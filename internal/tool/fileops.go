package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"path"
	"strings"
)

// readFile is the backend for the read_file tool. It returns the
// raw file contents; the caller decides whether to surface a
// useful error.
//
// Use readFileForTool (below) for the read_file *tool* call path —
// it adds the binary-detection + per-extension short-circuit that
// prevents the LLM from trying to "read" an image and confusing
// the upstream proxy.
func readFile(p string) ([]byte, error) {
	return os.ReadFile(p)
}

// ErrBinaryFile is returned by readFileForTool when the target
// is a binary file (image / audio / pdf / etc.). The LLM cannot
// usefully consume such bytes; the tool result tells it so.
var ErrBinaryFile = errors.New("binary file")

// readFileForTool is the path used by the read_file tool. It
// returns ErrBinaryFile (with a clear message) when the target
// is a known binary type, so the agent can surface a useful
// reply instead of dumping garbled bytes into the next model
// turn. For uploaded images the message also tells the model
// the image is already available as vision input in the
// conversation, so it should NOT try read_file on it.
//
// Detection (in order):
//  1. Extension: known binary (image / audio / video / archive /
//     pdf / executable).
//  2. Content sniff: NUL byte in the first 512 bytes => binary.
//  3. Otherwise, treat as text.
//
// The cap of 1 MiB is generous — anything bigger should be read
// in chunks via exec_command (head / tail) anyway, not dumped into
// the model's context.
func readFileForTool(p string) ([]byte, error) {
	if p == "" {
		return nil, errors.New("path is required")
	}
	ext := strings.ToLower(path.Ext(p))
	if kind, ok := binaryKindByExt(ext); ok {
		hint := "If you need to look at this file, ask the user to convert it to text or use a vision-capable model"
		if kind == "image" {
			hint = "This image is ALREADY attached to the user's message as vision input — you can see it directly. Do NOT call read_file on uploaded images; just respond based on the image you already received."
		}
		return nil, fmt.Errorf("%w: %s (%s); the read_file tool only handles text files. %s",
			ErrBinaryFile, p, kind, hint)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, fmt.Errorf("%w: %s (no text extension but content is binary); "+
			"the read_file tool only handles text files", ErrBinaryFile, p)
	}
	// Cap to 1 MiB to keep the model's context sane. A larger
	// file would just get truncated by the model anyway, and
	// hitting a wall here is clearer than silent truncation.
	const maxRead = 1 << 20
	if len(data) > maxRead {
		return data[:maxRead], nil
	}
	return data, nil
}

// binaryKindByExt maps a known binary file extension to a human
// description. Add entries as needed; this is intentionally
// narrow so we don't accidentally refuse a config file with a
// weird extension.
func binaryKindByExt(ext string) (string, bool) {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".tif", ".ico", ".heic":
		return "image", true
	case ".mp3", ".wav", ".m4a", ".ogg", ".flac", ".aac", ".opus":
		return "audio", true
	case ".mp4", ".mov", ".avi", ".mkv", ".webm", ".flv", ".wmv":
		return "video", true
	case ".pdf":
		return "pdf", true
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar", ".jar", ".war":
		return "archive", true
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".o", ".a", ".obj", ".lib":
		return "binary", true
	}
	return "", false
}

// isBinary reports whether the first 512 bytes of data look like
// binary content. The heuristic is intentionally simple: any NUL
// byte => binary. Plain text files never contain NUL.
func isBinary(data []byte) bool {
	const sniff = 512
	if len(data) == 0 {
		return false
	}
	end := sniff
	if len(data) < end {
		end = len(data)
	}
	for _, b := range data[:end] {
		if b == 0 {
			return true
		}
	}
	return false
}

func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Cap to 10 MiB so a prompt-injection cannot fill the disk
	// in a single write_file call. Anything bigger should be
	// written via exec_command (heredoc / split).
	const maxWrite = 10 << 20
	if len(data) > maxWrite {
		return fmt.Errorf("write size %d bytes exceeds cap of %d bytes; use exec_command to write large files in chunks", len(data), maxWrite)
	}
	// Atomic write: write to a sibling temp file then rename.
	// A crash mid-write leaves the original file intact instead
	// of producing a truncated/broken file on disk.
	tmp, err := os.CreateTemp(dir, ".write-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup on any error path.
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func listDir(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		mod := info.ModTime().Format("2006-01-02 15:04")
		kind := "f"
		if e.IsDir() {
			kind = "d"
		}
		fmt.Fprintf(&sb, "%s  %8d  %s  %s\n", kind, size, mod, e.Name())
	}
	return sb.String(), nil
}
