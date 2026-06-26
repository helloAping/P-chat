package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Indexer walks a knowledge directory, splits files into chunks, and
// stores their embeddings in the SQLite memory store. Re-indexing the
// same file is idempotent: we hash file content + chunk text and skip
// entries that are already up to date.
type Indexer struct {
	db        *sql.DB
	embedder  Embedder
	chunkSize int // target characters per chunk
	overlap   int // overlap between adjacent chunks
}

func NewIndexer(db *sql.DB, emb Embedder) *Indexer {
	return &Indexer{
		db:        db,
		embedder:  emb,
		chunkSize: 800,
		overlap:   100,
	}
}

// IndexDir walks dir recursively and indexes every .md / .txt / .markdown
// file. Returns the number of chunks created.
func (ix *Indexer) IndexDir(ctx context.Context, dir string) (int, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, fmt.Errorf("stat %s: %w", dir, err)
	}

	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}

	var total int
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden dirs and common noise.
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" && ext != ".markdown" {
			return nil
		}
		n, err := ix.IndexFile(ctx, path)
		if err != nil {
			fmt.Printf("warn: index %s: %v\n", path, err)
		}
		total += n
		return nil
	})
	return total, err
}

// IndexFile reads a single file and indexes its chunks.
func (ix *Indexer) IndexFile(ctx context.Context, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	text := string(data)
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}

	source := "kb:" + path
	fileHash := sha256.Sum256(data)
	fileHashHex := hex.EncodeToString(fileHash[:])[:16]

	// Detect if the file is already indexed with the same hash.
	var prevMeta string
	err = ix.db.QueryRow(
		`SELECT metadata FROM chunks WHERE source = ? ORDER BY id DESC LIMIT 1`,
		source,
	).Scan(&prevMeta)
	if err == nil {
		// Use map[string]any because chunk/chars are numbers, not strings.
		var meta map[string]any
		if decodeJSONMeta(prevMeta, &meta) == nil {
			if h, ok := meta["file_hash"].(string); ok && h == fileHashHex {
				// File is unchanged; skip.
				return 0, nil
			}
		}
		// Changed: drop old chunks.
		if _, err := ix.db.Exec(`DELETE FROM chunks WHERE source = ?`, source); err != nil {
			return 0, err
		}
	}

	chunks := SplitText(text, ix.chunkSize, ix.overlap)
	if len(chunks) == 0 {
		return 0, nil
	}

	// Embed in batches.
	embs, err := ix.embedder.EmbedBatch(ctx, chunks)
	if err != nil {
		return 0, fmt.Errorf("embed: %w", err)
	}

	tx, err := ix.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	chunkStmt, err := tx.Prepare(
		`INSERT INTO chunks(source, content, metadata, created_at) VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, err
	}
	defer chunkStmt.Close()
	embStmt, err := tx.Prepare(
		`INSERT INTO embeddings(chunk_id, model, vector, dim, created_at) VALUES (?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, err
	}
	defer embStmt.Close()

	for i, ch := range chunks {
		meta := fmt.Sprintf(`{"file_hash":%q,"chunk":%d,"chars":%d}`, fileHashHex, i, len(ch))
		res, err := chunkStmt.Exec(source, ch, meta, now)
		if err != nil {
			return 0, err
		}
		id, _ := res.LastInsertId()
		vec := embs[i]
		if _, err := embStmt.Exec(id, ix.embedder.Name(), float32sToBytes(vec), len(vec), now); err != nil {
			return 0, err
		}
	}
	return len(chunks), tx.Commit()
}

// SplitText splits text into overlapping chunks of approximately
// chunkSize characters. Splits happen on paragraph boundaries when
// possible, then on sentences, then on hard cuts.
func SplitText(text string, chunkSize, overlap int) []string {
	if chunkSize <= 0 {
		chunkSize = 800
	}
	if overlap < 0 || overlap >= chunkSize {
		overlap = chunkSize / 8
	}
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		return nil
	}
	if len(text) <= chunkSize {
		return []string{text}
	}

	var chunks []string
	for start := 0; start < len(text); {
		end := start + chunkSize
		if end >= len(text) {
			chunks = append(chunks, strings.TrimSpace(text[start:]))
			break
		}
		// Try to break on a paragraph or sentence boundary.
		breakAt := -1
		for _, sep := range []string{"\n\n", "。\n", ".\n", "!\n", "?\n", "。", "!\n"} {
			if i := strings.LastIndex(text[start:end], sep); i > 0 {
				breakAt = start + i + len(sep)
				break
			}
		}
		if breakAt <= start {
			breakAt = end
		}
		chunks = append(chunks, strings.TrimSpace(text[start:breakAt]))
		start = breakAt - overlap
		if start <= chunksIndexStart(chunks, overlap) {
			start = breakAt // avoid infinite loop
		}
	}
	return chunks
}

func chunksIndexStart(chunks []string, overlap int) int {
	if len(chunks) == 0 {
		return 0
	}
	return len(chunks[len(chunks)-1]) - overlap
}

// SearchResult is one hit from the retriever.
type SearchResult struct {
	ChunkID    int64
	Source     string
	Content    string
	Similarity float32
	Rank       int
}

// Retriever finds the top-k chunks most similar to a query.
type Retriever struct {
	db       *sql.DB
	embedder Embedder
}

func NewRetriever(db *sql.DB, emb Embedder) *Retriever {
	return &Retriever{db: db, embedder: emb}
}

// Search embeds the query and returns up to topK chunks ranked by
// cosine similarity. If sourcePrefix is non-empty, only chunks whose
// source starts with that prefix are considered (e.g. "kb:").
func (r *Retriever) Search(ctx context.Context, query string, topK int, sourcePrefix string) ([]SearchResult, error) {
	if topK <= 0 {
		topK = 5
	}
	qvec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	var (
		rows *sql.Rows
	)
	if sourcePrefix != "" {
		rows, err = r.db.Query(
			`SELECT c.id, c.source, c.content, e.vector, e.dim
			 FROM chunks c JOIN embeddings e ON c.id = e.chunk_id
			 WHERE c.source LIKE ?`,
			sourcePrefix+"%",
		)
	} else {
		rows, err = r.db.Query(
			`SELECT c.id, c.source, c.content, e.vector, e.dim
			 FROM chunks c JOIN embeddings e ON c.id = e.chunk_id`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		id     int64
		source string
		text   string
		score  float32
	}
	var all []scored
	for rows.Next() {
		var (
			id     int64
			source string
			text   string
			vecB   []byte
			dim    int
		)
		if err := rows.Scan(&id, &source, &text, &vecB, &dim); err != nil {
			return nil, err
		}
		vec := bytesToFloat32s(vecB, dim)
		sim := CosineSimilarity(qvec, vec)
		all = append(all, scored{id: id, source: source, text: text, score: sim})
	}

	// Top-k via simple sort. n is usually small.
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].score > all[i].score {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if len(all) > topK {
		all = all[:topK]
	}
	out := make([]SearchResult, len(all))
	for i, s := range all {
		out[i] = SearchResult{
			ChunkID:    s.id,
			Source:     s.source,
			Content:    s.text,
			Similarity: s.score,
			Rank:       i + 1,
		}
	}
	return out, nil
}

func float32sToBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], mathFloat32bits(x))
	}
	return b
}

func bytesToFloat32s(b []byte, dim int) []float32 {
	out := make([]float32, dim)
	for i := 0; i < dim; i++ {
		out[i] = mathFloat32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func decodeJSONMeta(s string, dst any) error {
	return jsonDecodeString(s, dst)
}
