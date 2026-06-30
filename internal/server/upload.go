package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/paths"
)

// uploadKind classifies an uploaded file for the UI. The LLM layer
// uses this to decide how to feed the file to the model:
//
//   image  → OpenAI image_url content block (vision)
//   audio  → text marker (no native audio wire block in the
//            adapters yet — see ExpandAttachmentsCM)
//   video  → text marker (no native video wire block in any
//            adapter today; rendered in the chat bubble as a
//            <video> element so the user can still preview it)
//   text   → text/* or anything we can read as text; appended to
//            the user message as a code-fenced block
//   file   → unknown binary; also appended as a textual marker
//            "filename: <name>, size: N" so the model at least
//            knows the user attached something.
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

// UploadMeta is the public alias used by the JSON response and
// by external test packages.
type UploadMeta = uploadMeta

// maxUploadSize is 25 MB. Picked to match OpenAI's own file upload
// limit; well under gin's default 32 MB so the multipart parser
// never gets close to that ceiling.
const maxUploadSize = 25 << 20

// UploadDir returns ~/.p-chat/uploads/. Created on first use.
func UploadDir() string {
	return filepath.Join(paths.GlobalDir(), "uploads")
}

// POST /api/v1/uploads �?multipart/form-data with a single "file"
// field. Returns the metadata (id, name, size, kind, mime). The
// file lives on disk; the client passes the id back when sending
// a message and the server reads it from disk.
func (h *Handler) Upload(c *gin.Context) {
	if err := os.MkdirAll(UploadDir(), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mkdir: " + err.Error()})
		return
	}
	// Cap the request body so a malicious client can't fill the disk.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)

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
	written, err := io.Copy(out, f)
	out.Close()
	if err != nil {
		_ = os.Remove(stored)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write: " + err.Error()})
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

// GET /api/v1/uploads/:id �?serve the raw file back. Used by the
// web UI for thumbnail previews and by future LLM clients that
// prefer HTTP URLs over inline base64.
func (h *Handler) GetUpload(c *gin.Context) {
	id := c.Param("id")
	if !validUploadID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
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
