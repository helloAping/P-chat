package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/paths"
)

// uploadKind classifies an uploaded file for the UI. The LLM layer
// uses this to decide how to feed the file to the model:
//
//	image  → OpenAI image_url content block (vision)
//	audio  → native input when the active model declares audio support,
//	         otherwise a media_recognize upload reference
//	video  → native input when the active model declares video support,
//	         otherwise a media_recognize upload reference
//	text   → a read_attachment upload reference
//	file   → read_attachment for supported PDF/Office formats, otherwise
//	         a clear unsupported-format marker
type uploadKind string

const (
	kindImage uploadKind = "image"
	kindAudio uploadKind = "audio"
	kindVideo uploadKind = "video"
	kindText  uploadKind = "text"
	kindFile  uploadKind = "file"
)

type uploadMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	MIME     string `json:"mime"`
	StoredAs string `json:"-"`
}

// uploadOwner records the conversation that is allowed to attach an upload.
// It intentionally lives next to the upload bytes rather than in SQLite: an
// upload may be created before a message row exists.
type uploadOwner struct {
	SessionID string `json:"session_id"`
}

// UploadMeta is the public alias used by the JSON response and
// by external test packages.
type UploadMeta = uploadMeta

// maxUploadSize is 25 MB. Picked to match OpenAI's own file upload
// limit; well under gin's default 32 MB so the multipart parser
// never gets close to that ceiling.
const maxUploadSize = 25 << 20
const maxUploadRequestSize = maxUploadSize + (1 << 20)

// UploadDir returns ~/.p-chat/uploads/. Created on first use.
func UploadDir() string {
	return filepath.Join(paths.GlobalDir(), "uploads")
}

// pruneUploadFiles deletes the on-disk files for upload ids that are
// NO LONGER referenced by any message row. Called after a destructive
// delete (permanent session delete / clear session messages) so the
// orphaned image bytes don't accumulate forever. Never called from the
// rollback path — undo restores the messages, which would then 404 on
// the pruned file. Reference counting lives in the store
// (CountUploadRefs), which counts across ALL conversations.
func (h *Handler) pruneUploadFiles(ids []string) {
	if h.store == nil {
		return
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if h.store.CountUploadRefs(id) > 0 || h.store.CountPendingTurnQueueUploadRefs(id) > 0 {
			continue // still referenced elsewhere — keep the file
		}
		entries, err := os.ReadDir(UploadDir())
		if err != nil {
			continue
		}
		prefix := id + "-"
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			_ = os.Remove(filepath.Join(UploadDir(), e.Name()))
		}
		_ = os.Remove(uploadOwnerPath(id))
	}
}

// sweepOrphanUploads scans ~/.p-chat/uploads and deletes files whose
// upload id is referenced by no message row AND whose mtime is older
// than the grace period. The grace period protects files uploaded but
// not yet sent in a message (the SPA POSTs /uploads first, then ships
// the id with the next message) — a user who uploads an image and
// abandons it for > grace days is a genuine orphan. Returns the number
// of files removed. Best-effort; errors are ignored.
func (h *Handler) sweepOrphanUploads(grace time.Duration) int {
	if h.store == nil {
		return 0
	}
	entries, err := os.ReadDir(UploadDir())
	if err != nil {
		return 0
	}
	removed := 0
	cutoff := time.Now().Add(-grace)
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".owner.json") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		// Filename is "<16-hex-id>-<name>". The id is the part
		// before the first dash; upload ids are exactly 16 hex chars.
		name := e.Name()
		idx := strings.IndexByte(name, '-')
		if idx < 0 {
			continue
		}
		id := name[:idx]
		if !validUploadID(id) {
			continue
		}
		if h.store.CountUploadRefs(id) > 0 || h.store.CountPendingTurnQueueUploadRefs(id) > 0 {
			continue
		}
		_ = os.Remove(filepath.Join(UploadDir(), name))
		_ = os.Remove(uploadOwnerPath(id))
		removed++
	}
	return removed
}

// POST /api/v1/uploads �?multipart/form-data with a single "file"
// field. Returns the metadata (id, name, size, kind, mime). The
// file lives on disk; the client passes the id back when sending
// a message and the server reads it from disk.
func (h *Handler) Upload(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	// Install the cap before any multipart accessor parses the body.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadRequestSize)
	sessionID := strings.TrimSpace(c.PostForm("session_id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}
	if _, err := h.store.GetConversation(sessionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(UploadDir(), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mkdir: " + err.Error()})
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file: " + err.Error()})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open: " + err.Error()})
		return
	}
	defer f.Close()

	id := newUploadID()
	stored := filepath.Join(UploadDir(), id+"-"+filepath.Base(fh.Filename))
	out, err := os.Create(stored)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create: " + err.Error()})
		return
	}
	written, err := io.Copy(out, io.LimitReader(f, maxUploadSize+1))
	out.Close()
	if err != nil {
		_ = os.Remove(stored)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write: " + err.Error()})
		return
	}
	if written == 0 {
		_ = os.Remove(stored)
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty file"})
		return
	}
	if written > maxUploadSize {
		_ = os.Remove(stored)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file too large: max %d bytes", maxUploadSize)})
		return
	}
	if err := writeUploadOwner(id, sessionID); err != nil {
		_ = os.Remove(stored)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "record upload owner: " + err.Error()})
		return
	}

	meta := uploadMeta{
		ID:       id,
		Name:     fh.Filename,
		Size:     written,
		Kind:     string(classifyUpload(fh.Filename, fh.Header.Get("Content-Type"))),
		MIME:     detectMIME(fh.Filename, fh.Header.Get("Content-Type")),
		StoredAs: stored,
	}
	c.JSON(http.StatusCreated, meta)
}

func uploadOwnerPath(id string) string {
	return filepath.Join(UploadDir(), id+".owner.json")
}

func writeUploadOwner(id, sessionID string) error {
	data, err := json.Marshal(uploadOwner{SessionID: sessionID})
	if err != nil {
		return err
	}
	return os.WriteFile(uploadOwnerPath(id), data, 0o600)
}

func readUploadOwner(id string) (string, bool, error) {
	data, err := os.ReadFile(uploadOwnerPath(id))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var owner uploadOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return "", false, err
	}
	return owner.SessionID, true, nil
}

// validateAttachmentOwnership rejects unresolved or cross-session upload
// references before they reach the agent. Legacy uploads without an owner
// sidecar remain readable only when this session already persisted the same
// upl:// reference, preserving existing chat history without reopening the
// global upload namespace for new messages.
func (h *Handler) validateAttachmentOwnership(sessionID string, atts []agent.Attachment) error {
	legacyRefs := make(map[string]struct{})
	for _, id := range h.store.UploadRefsForConversation(sessionID) {
		legacyRefs[id] = struct{}{}
	}
	for _, att := range atts {
		id := strings.TrimSpace(att.UploadID)
		if id == "" {
			id = strings.TrimSpace(att.ID)
		}
		if id == "" {
			continue
		}
		if !validUploadID(id) {
			return fmt.Errorf("attachment %q has invalid upload_id", att.Name)
		}
		owner, found, err := readUploadOwner(id)
		if err != nil {
			return fmt.Errorf("read attachment %q owner: %w", att.Name, err)
		}
		if found && owner != sessionID {
			return fmt.Errorf("attachment %q belongs to another session", att.Name)
		}
		if !found {
			if _, ok := legacyRefs[id]; !ok {
				return fmt.Errorf("attachment %q is not available in this session; upload it again", att.Name)
			}
		}
		if path, _ := (&agent.DiskAttachmentResolver{BaseDir: UploadDir()}).Resolve(agent.Attachment{ID: id}); path == "" {
			return fmt.Errorf("attachment %q bytes are missing; upload it again", att.Name)
		}
	}
	return nil
}

// materializeInlineFileAttachments upgrades the legacy inline document
// fallback into a normal, session-owned upload before the agent sees it.
// Binary documents must use a data URL; plain Text is accepted for callers
// that provide a textual file body. Other opaque inline encodings are
// rejected instead of pretending the document can later be read by upload id.
func (h *Handler) materializeInlineFileAttachments(sessionID string, atts []agent.Attachment) (err error) {
	if len(atts) == 0 {
		return nil
	}
	if err := os.MkdirAll(UploadDir(), 0o755); err != nil {
		return fmt.Errorf("create upload directory: %w", err)
	}
	type materializedAttachment struct {
		index int
		value agent.Attachment
		path  string
		owner string
	}
	created := make([]materializedAttachment, 0, len(atts))
	defer func() {
		if err == nil {
			return
		}
		for _, item := range created {
			_ = os.Remove(item.path)
			_ = os.Remove(item.owner)
		}
	}()
	for i := range atts {
		a := atts[i]
		if (a.Kind != string(kindFile) && a.Kind != string(kindText)) || a.UploadID != "" || a.ID != "" {
			continue
		}
		payload := a.Data
		if payload == "" {
			payload = a.URL
		}
		if payload == "" {
			payload = a.Text
		}
		if payload == "" {
			continue
		}
		plainText := a.Kind == string(kindText) && !strings.HasPrefix(payload, "data:")
		data, mime, err := decodeInlineFilePayload(payload, plainText)
		if err != nil {
			return fmt.Errorf("attachment %q inline fallback: %w", a.Name, err)
		}
		if len(data) == 0 || len(data) > maxUploadSize {
			return fmt.Errorf("attachment %q inline fallback has invalid size", a.Name)
		}
		name := filepath.Base(strings.TrimSpace(a.Name))
		if name == "." || name == "" {
			return fmt.Errorf("attachment inline fallback requires a filename")
		}
		id := newUploadID()
		stored := filepath.Join(UploadDir(), id+"-"+name)
		if err := os.WriteFile(stored, data, 0o600); err != nil {
			return fmt.Errorf("write attachment %q: %w", name, err)
		}
		if err := writeUploadOwner(id, sessionID); err != nil {
			_ = os.Remove(stored)
			return fmt.Errorf("record attachment %q owner: %w", name, err)
		}
		a.ID, a.UploadID, a.Size = id, id, int64(len(data))
		if a.MIME == "" {
			a.MIME = mime
		}
		a.Data, a.URL, a.Text = "", "", ""
		created = append(created, materializedAttachment{index: i, value: a, path: stored, owner: uploadOwnerPath(id)})
	}
	for _, item := range created {
		atts[item.index] = item.value
	}
	return nil
}

func decodeInlineFilePayload(payload string, plainText bool) ([]byte, string, error) {
	if plainText {
		return []byte(payload), "text/plain", nil
	}
	if !strings.HasPrefix(payload, "data:") {
		return nil, "", fmt.Errorf("must be a data URL")
	}
	comma := strings.IndexByte(payload, ',')
	if comma < 0 {
		return nil, "", fmt.Errorf("malformed data URL")
	}
	header, body := payload[5:comma], payload[comma+1:]
	mime := strings.Split(header, ";")[0]
	if strings.Contains(header, ";base64") {
		data, err := base64.StdEncoding.DecodeString(body)
		return data, mime, err
	}
	data, err := url.PathUnescape(body)
	return []byte(data), mime, err
}

// GET /api/v1/uploads/:id �?serve the raw file back. Used by the
// web UI for thumbnail previews and by future LLM clients that
// prefer HTTP URLs over inline base64.
func (h *Handler) GetUpload(c *gin.Context) {
	id := c.Param("id")
	if !validUploadID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return
	}
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	if _, err := h.store.GetConversation(sessionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if err := h.validateAttachmentOwnership(sessionID, []agent.Attachment{{ID: id, UploadID: id}}); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "upload is not available in this session"})
		return
	}
	dir := UploadDir()
	_ = os.MkdirAll(dir, 0o755)
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	prefix := id + "-"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ct := mimeByExt(ext); ct != "" {
			c.Header("Content-Type", ct)
		}
		c.File(fullPath)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
}

func mimeByExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".m4v":
		return "video/x-m4v"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".xml":
		return "application/xml"
	case ".html", ".htm":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

// newUploadID returns 16 hex chars of randomness. Plenty for
// preventing accidental guessing; not a security boundary.
func newUploadID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand on linux/macOS only fails if the kernel
		// entropy source is dead. Fall back to a fixed prefix so
		// the request still gets a unique-ish id.
		return fmt.Sprintf("upl_%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}

func validUploadID(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// classifyUpload picks image / audio / video / text / file
// based on the extension and the Content-Type header. The MIME
// header is advisory; the extension wins for the common cases
// where the browser sends a generic "application/octet-stream".
func classifyUpload(name, mime string) uploadKind {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return kindImage
	case ".mp3", ".wav", ".m4a", ".ogg", ".flac", ".opus", ".aac", ".pcm":
		return kindAudio
	case ".mp4", ".webm", ".mov", ".mkv", ".m4v":
		return kindVideo
	case ".txt", ".md", ".csv", ".json", ".yaml", ".yml", ".xml", ".html", ".htm",
		".js", ".ts", ".tsx", ".jsx", ".go", ".py", ".rs", ".java", ".c", ".cpp",
		".h", ".hpp", ".cs", ".rb", ".php", ".sh", ".bash", ".zsh", ".ps1",
		".ini", ".toml", ".env", ".log", ".sql":
		return kindText
	}
	if strings.HasPrefix(mime, "image/") {
		return kindImage
	}
	if strings.HasPrefix(mime, "audio/") {
		return kindAudio
	}
	if strings.HasPrefix(mime, "video/") {
		return kindVideo
	}
	if strings.HasPrefix(mime, "text/") || strings.HasPrefix(mime, "application/json") {
		return kindText
	}
	return kindFile
}

// detectMIME picks the MIME type for a file. Extension-based
// detection is preferred because browsers often send a generic
// "application/octet-stream" for common image formats. Falls back
// to the browser-supplied MIME when the extension is unknown.
func detectMIME(name, browserMIME string) string {
	if ct := mimeByExt(strings.ToLower(filepath.Ext(name))); ct != "" {
		return ct
	}
	if browserMIME != "" {
		return browserMIME
	}
	return "application/octet-stream"
}
