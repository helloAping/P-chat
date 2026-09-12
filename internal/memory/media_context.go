package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// MediaContextKindRecognition records OCR/vision/audio/video analysis output.
	MediaContextKindRecognition = "recognition"
	// MediaContextKindGeneration records prompts and asset refs from media generation.
	MediaContextKindGeneration = "generation"
)

// MediaContext is a durable, bounded record of one media recognition or
// generation tool call. It lets later turns reuse a prior media result without
// pretending that the original tool call is still live.
type MediaContext struct {
	ID             string
	SessionID      string
	Kind           string
	ToolName       string
	InputRefs      []string
	OutputRefs     []string
	Prompt         string
	ResultText     string
	Summary        string
	StructuredJSON string
	ContextRefs    []string
	ToolCallID     string
	MessageID      int64
	RegenGroupID   string
	Archived       bool
	ArchiveReason  string
	CreatedAt      time.Time
}

// AddMediaContext persists a media tool context immediately and returns its ID.
func (s *Store) AddMediaContext(ctx MediaContext) (string, error) {
	if s == nil {
		return "", fmt.Errorf("store is nil")
	}
	if ctx.SessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	if ctx.Kind == "" {
		return "", fmt.Errorf("media context kind is required")
	}
	if ctx.ID == "" {
		ctx.ID = newMediaContextID()
	}
	if ctx.CreatedAt.IsZero() {
		ctx.CreatedAt = time.Now()
	}
	inputRefs, err := json.Marshal(ctx.InputRefs)
	if err != nil {
		return "", fmt.Errorf("encode input refs: %w", err)
	}
	outputRefs, err := json.Marshal(ctx.OutputRefs)
	if err != nil {
		return "", fmt.Errorf("encode output refs: %w", err)
	}
	contextRefs, err := json.Marshal(ctx.ContextRefs)
	if err != nil {
		return "", fmt.Errorf("encode context refs: %w", err)
	}
	archived := 0
	if ctx.Archived {
		archived = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(
		`INSERT INTO media_contexts (
			    id, session_id, kind, tool_name, input_refs_json, output_refs_json,
			    prompt, result_text, summary, structured_json, context_refs_json,
			    tool_call_id, message_id, regen_group_id, archived, archive_reason, created_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ctx.ID, ctx.SessionID, ctx.Kind, ctx.ToolName, string(inputRefs), string(outputRefs),
		ctx.Prompt, ctx.ResultText, ctx.Summary, ctx.StructuredJSON, string(contextRefs),
		ctx.ToolCallID, ctx.MessageID, ctx.RegenGroupID, archived, ctx.ArchiveReason, ctx.CreatedAt.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("insert media context: %w", err)
	}
	return ctx.ID, nil
}

// RecentMediaContexts returns active media contexts for a session, oldest first.
func (s *Store) RecentMediaContexts(sessionID string, limit int) ([]MediaContext, error) {
	if s == nil || sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.Query(
		`SELECT id, session_id, kind, tool_name, input_refs_json, output_refs_json,
			        prompt, result_text, summary, structured_json, context_refs_json,
			        tool_call_id, message_id, regen_group_id, archived, archive_reason, created_at
			   FROM media_contexts
			  WHERE session_id = ? AND archived = 0
			  ORDER BY created_at DESC, id DESC
		  LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent media contexts: %w", err)
	}
	defer rows.Close()

	contexts := make([]MediaContext, 0, limit)
	for rows.Next() {
		ctx, err := scanMediaContext(rows)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, ctx)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(contexts)-1; i < j; i, j = i+1, j-1 {
		contexts[i], contexts[j] = contexts[j], contexts[i]
	}
	return contexts, nil
}

// RecentMediaContextsByKinds returns active recent contexts matching one of the
// provided kinds. Unknown/empty kinds are ignored; no valid kinds falls back to
// RecentMediaContexts.
func (s *Store) RecentMediaContextsByKinds(sessionID string, kinds []string, limit int) ([]MediaContext, error) {
	if s == nil || sessionID == "" {
		return nil, nil
	}
	cleanKinds := make([]string, 0, len(kinds))
	seen := map[string]struct{}{}
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		cleanKinds = append(cleanKinds, kind)
	}
	if len(cleanKinds) == 0 {
		return s.RecentMediaContexts(sessionID, limit)
	}
	if limit <= 0 {
		limit = 8
	}
	placeholders := make([]string, len(cleanKinds))
	args := make([]any, 0, 2+len(cleanKinds))
	args = append(args, sessionID)
	for i, kind := range cleanKinds {
		placeholders[i] = "?"
		args = append(args, kind)
	}
	args = append(args, limit)
	rows, err := s.db.Query(
		fmt.Sprintf(`SELECT id, session_id, kind, tool_name, input_refs_json, output_refs_json,
			        prompt, result_text, summary, structured_json, context_refs_json,
			        tool_call_id, message_id, regen_group_id, archived, archive_reason, created_at
			   FROM media_contexts
			  WHERE session_id = ? AND archived = 0 AND kind IN (%s)
			  ORDER BY created_at DESC, id DESC
			  LIMIT ?`, strings.Join(placeholders, ",")),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent media contexts by kind: %w", err)
	}
	defer rows.Close()
	contexts := make([]MediaContext, 0, limit)
	for rows.Next() {
		ctx, err := scanMediaContext(rows)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, ctx)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(contexts)-1; i < j; i, j = i+1, j-1 {
		contexts[i], contexts[j] = contexts[j], contexts[i]
	}
	return contexts, nil
}

// ResolveMediaContextGraph resolves explicit context ids and their parent
// context_refs, returning parents before children. Only active contexts in the
// same session are visible.
func (s *Store) ResolveMediaContextGraph(sessionID string, refs []string, maxDepth, maxNodes int) ([]MediaContext, error) {
	if s == nil || sessionID == "" {
		return nil, nil
	}
	refs = normalizeMediaContextIDRefs(refs)
	if len(refs) == 0 {
		return nil, nil
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxNodes <= 0 {
		maxNodes = 12
	}
	seen := map[string]struct{}{}
	visiting := map[string]struct{}{}
	out := make([]MediaContext, 0, len(refs))
	var walk func(string, int) error
	walk = func(id string, depth int) error {
		if _, ok := seen[id]; ok {
			return nil
		}
		if depth > maxDepth {
			return fmt.Errorf("media context %s exceeds max reference depth %d", id, maxDepth)
		}
		if len(out) >= maxNodes {
			return fmt.Errorf("media context graph exceeds max nodes %d", maxNodes)
		}
		if _, ok := visiting[id]; ok {
			return nil
		}
		visiting[id] = struct{}{}
		ctx, err := s.mediaContextByID(sessionID, id)
		if err != nil {
			delete(visiting, id)
			return err
		}
		for _, parent := range normalizeMediaContextIDRefs(ctx.ContextRefs) {
			if err := walk(parent, depth+1); err != nil {
				delete(visiting, id)
				return err
			}
		}
		delete(visiting, id)
		seen[id] = struct{}{}
		out = append(out, ctx)
		return nil
	}
	for _, ref := range refs {
		if err := walk(ref, 1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ArchiveMediaContextsForRegenGroup hides contexts produced by an older
// regenerated sibling before the replacement sibling starts creating new ones.
func (s *Store) ArchiveMediaContextsForRegenGroup(sessionID, groupID string) error {
	if s == nil || sessionID == "" || groupID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE media_contexts
			    SET archived = 1, archive_reason = 'regen'
			  WHERE session_id = ? AND regen_group_id = ?`,
		sessionID, groupID,
	)
	if err != nil {
		return fmt.Errorf("archive media contexts for regen group: %w", err)
	}
	return nil
}

// AssignPendingMediaContextsToMessage attaches active contexts that were
// created before the assistant sibling row existed, such as current-turn image
// preflight results.
func (s *Store) AssignPendingMediaContextsToMessage(sessionID, groupID string, messageID int64) error {
	if s == nil || sessionID == "" || groupID == "" || messageID <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`UPDATE media_contexts
		    SET message_id = ?
		  WHERE session_id = ?
		    AND regen_group_id = ?
		    AND message_id = 0
		    AND archived = 0`,
		messageID, sessionID, groupID,
	)
	if err != nil {
		return fmt.Errorf("assign pending media contexts: %w", err)
	}
	return nil
}

func (s *Store) mediaContextByID(sessionID, id string) (MediaContext, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, kind, tool_name, input_refs_json, output_refs_json,
		        prompt, result_text, summary, structured_json, context_refs_json,
		        tool_call_id, message_id, regen_group_id, archived, archive_reason, created_at
		   FROM media_contexts
		  WHERE session_id = ? AND id = ? AND archived = 0`,
		sessionID, id,
	)
	ctx, err := scanMediaContext(row)
	if err == sql.ErrNoRows {
		return MediaContext{}, fmt.Errorf("media context %q is not active in this session", id)
	}
	return ctx, err
}

type mediaContextScanner interface {
	Scan(dest ...any) error
}

func scanMediaContext(row mediaContextScanner) (MediaContext, error) {
	var (
		ctx         MediaContext
		inputJSON   string
		outputJSON  string
		contextJSON string
		archived    int
		createdAt   int64
	)
	if err := row.Scan(
		&ctx.ID, &ctx.SessionID, &ctx.Kind, &ctx.ToolName, &inputJSON, &outputJSON,
		&ctx.Prompt, &ctx.ResultText, &ctx.Summary, &ctx.StructuredJSON, &contextJSON,
		&ctx.ToolCallID, &ctx.MessageID, &ctx.RegenGroupID, &archived, &ctx.ArchiveReason, &createdAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return MediaContext{}, err
		}
		return MediaContext{}, fmt.Errorf("scan media context: %w", err)
	}
	ctx.InputRefs = decodeStringSlice(inputJSON)
	ctx.OutputRefs = decodeStringSlice(outputJSON)
	ctx.ContextRefs = decodeStringSlice(contextJSON)
	ctx.Archived = archived == 1
	ctx.CreatedAt = time.Unix(createdAt, 0)
	return ctx, nil
}

func decodeStringSlice(raw string) []string {
	var values []string
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}

func normalizeMediaContextIDRefs(refs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func newMediaContextID() string {
	return "mctx_" + uuid.NewString()
}
