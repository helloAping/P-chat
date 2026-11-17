package tool

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxJournalTurns         = 5
	maxJournalSnapshotBytes = 10 << 20 // match writeFile cap
)

type fileJournalKey struct{}

// FileJournal keeps per-session before-snapshots so the GUI can
// undo the latest agent turn's file mutations (AE-07).
type FileJournal struct {
	mu       sync.Mutex
	sessions map[string]*sessionFileJournal
}

type sessionFileJournal struct {
	turns []*fileTurnBatch
}

type fileTurnBatch struct {
	StartedAt time.Time
	byPath    map[string]int
	entries   []FileChangeEntry
	previews  []TurnPreview
}

// TurnPreview is a durable summary of one file or command touch.
// It does not include file bytes.
type TurnPreview struct {
	Tool    string
	Path    string
	Preview string
}

// FileChangeEntry is one path's first snapshot in a turn.
type FileChangeEntry struct {
	Path    string `json:"path"`
	Tool    string `json:"tool"`
	Existed bool   `json:"existed"`
	// Before is omitted from JSON responses (can be large); kept for undo.
	Before []byte `json:"-"`
}

// FileChangeSummary is the API/UI view of the latest undoable turn.
type FileChangeSummary struct {
	Count     int               `json:"count"`
	StartedAt time.Time         `json:"started_at"`
	Entries   []FileChangeEntry `json:"entries"`
}

// NewFileJournal constructs an empty journal.
func NewFileJournal() *FileJournal {
	return &FileJournal{sessions: make(map[string]*sessionFileJournal)}
}

// WithFileJournal stores the journal on the tool context.
func WithFileJournal(ctx context.Context, j *FileJournal) context.Context {
	if j == nil {
		return ctx
	}
	return context.WithValue(ctx, fileJournalKey{}, j)
}

func journalFromCtx(ctx context.Context) *FileJournal {
	if ctx == nil {
		return nil
	}
	if j, ok := ctx.Value(fileJournalKey{}).(*FileJournal); ok {
		return j
	}
	return nil
}

// BeginTurn opens a new undo batch for the session. Call once per
// user-driven agent turn before tools run.
func (j *FileJournal) BeginTurn(sessionID string) {
	if j == nil || sessionID == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sj := j.sessions[sessionID]
	if sj == nil {
		sj = &sessionFileJournal{}
		j.sessions[sessionID] = sj
	}
	sj.turns = append(sj.turns, &fileTurnBatch{
		StartedAt: time.Now().UTC(),
		byPath:    make(map[string]int),
	})
	if len(sj.turns) > maxJournalTurns {
		sj.turns = sj.turns[len(sj.turns)-maxJournalTurns:]
	}
}

// RecordBeforeMutation snapshots path once per turn (first write wins).
func (j *FileJournal) RecordBeforeMutation(sessionID, toolName, path string) {
	if j == nil || sessionID == "" || path == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		return
	}
	turn := sj.turns[len(sj.turns)-1]
	if _, ok := turn.byPath[path]; ok {
		return
	}
	entry := FileChangeEntry{Path: path, Tool: toolName}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > maxJournalSnapshotBytes {
			// Too large to restore safely — skip journaling this path.
			return
		}
		entry.Existed = true
		entry.Before = append([]byte(nil), data...)
	} else if !os.IsNotExist(err) {
		return
	}
	turn.byPath[path] = len(turn.entries)
	turn.entries = append(turn.entries, entry)
	turn.previews = append(turn.previews, TurnPreview{Tool: toolName, Path: path, Preview: path})
}

// RecordCommandPreview appends a command summary to the current turn.
// It does not create an undo snapshot.
func (j *FileJournal) RecordCommandPreview(sessionID, command string) {
	if j == nil || sessionID == "" || command == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		return
	}
	turn := sj.turns[len(sj.turns)-1]
	turn.previews = append(turn.previews, TurnPreview{
		Tool:    "exec_command",
		Preview: clipPreview(command, 160),
	})
}

// CurrentTurnPreviews copies summaries from the turn opened by the latest BeginTurn.
func (j *FileJournal) CurrentTurnPreviews(sessionID string) []TurnPreview {
	if j == nil || sessionID == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		return nil
	}
	turn := sj.turns[len(sj.turns)-1]
	if len(turn.previews) == 0 {
		return nil
	}
	out := make([]TurnPreview, len(turn.previews))
	copy(out, turn.previews)
	return out
}

func clipPreview(text string, maxRunes int) string {
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "…"
}

// RecordCommandPreviewFromCtx records a command on the journal attached to ctx.
func RecordCommandPreviewFromCtx(ctx context.Context, command string) {
	j := journalFromCtx(ctx)
	if j == nil {
		return
	}
	j.RecordCommandPreview(SessionIDFromCtx(ctx), command)
}

// PeekLastTurn returns a copy of the latest non-empty turn's entries
// (no Before bytes). Empty BeginTurn batches are skipped so the GUI
// banner stays aligned with UndoLastTurn.
func (j *FileJournal) PeekLastTurn(sessionID string) FileChangeSummary {
	if j == nil || sessionID == "" {
		return FileChangeSummary{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		return FileChangeSummary{}
	}
	var turn *fileTurnBatch
	for i := len(sj.turns) - 1; i >= 0; i-- {
		if len(sj.turns[i].entries) > 0 {
			turn = sj.turns[i]
			break
		}
	}
	if turn == nil {
		return FileChangeSummary{}
	}
	out := make([]FileChangeEntry, len(turn.entries))
	for i, e := range turn.entries {
		out[i] = FileChangeEntry{Path: e.Path, Tool: e.Tool, Existed: e.Existed}
	}
	return FileChangeSummary{
		Count:     len(out),
		StartedAt: turn.StartedAt,
		Entries:   out,
	}
}

// UndoLastTurn restores files from the latest non-empty turn and pops it.
func (j *FileJournal) UndoLastTurn(sessionID string) (FileChangeSummary, error) {
	if j == nil || sessionID == "" {
		return FileChangeSummary{}, fmt.Errorf("no file journal")
	}
	j.mu.Lock()
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		j.mu.Unlock()
		return FileChangeSummary{}, fmt.Errorf("nothing to undo")
	}
	// Find last turn with entries.
	idx := -1
	for i := len(sj.turns) - 1; i >= 0; i-- {
		if len(sj.turns[i].entries) > 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		j.mu.Unlock()
		return FileChangeSummary{}, fmt.Errorf("nothing to undo")
	}
	turn := sj.turns[idx]
	// Copy entries then drop the turn before unlocking for IO.
	entries := append([]FileChangeEntry(nil), turn.entries...)
	sj.turns = append(sj.turns[:idx], sj.turns[idx+1:]...)
	started := turn.StartedAt
	j.mu.Unlock()

	var errs []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !e.Existed {
			if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("%s: %v", e.Path, err))
			}
			continue
		}
		if err := writeFile(e.Path, e.Before); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Path, err))
		}
	}
	summary := FileChangeSummary{
		Count:     len(entries),
		StartedAt: started,
		Entries:   make([]FileChangeEntry, len(entries)),
	}
	for i, e := range entries {
		summary.Entries[i] = FileChangeEntry{Path: e.Path, Tool: e.Tool, Existed: e.Existed}
	}
	if len(errs) > 0 {
		return summary, fmt.Errorf("partial undo: %s", errs[0])
	}
	return summary, nil
}

// SessionIDFromCtx returns the session id attached via WithSessionID.
func SessionIDFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if sid, ok := ctx.Value(SessionIDKey{}).(string); ok {
		return sid
	}
	return ""
}

// RecordBeforeMutationFromCtx snapshots using journal + session on ctx.
func RecordBeforeMutationFromCtx(ctx context.Context, toolName, path string) {
	j := journalFromCtx(ctx)
	if j == nil {
		return
	}
	j.RecordBeforeMutation(SessionIDFromCtx(ctx), toolName, path)
}
