package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
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
	// Added and Deleted are line counts versus the pre-change snapshot.
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
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

// ForgetUnchanged drops current-turn snapshots whose files still match
// the saved bytes. Shell delete targets are recorded before the command
// runs; this removes the ones the command left untouched.
func (j *FileJournal) ForgetUnchanged(sessionID string, paths []string) {
	if j == nil || sessionID == "" || len(paths) == 0 {
		return
	}
	want := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path != "" {
			want[path] = struct{}{}
		}
	}
	if len(want) == 0 {
		return
	}
	j.mu.Lock()
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		j.mu.Unlock()
		return
	}
	turn := sj.turns[len(sj.turns)-1]
	candidates := make([]FileChangeEntry, 0, len(want))
	for _, entry := range turn.entries {
		if _, ok := want[entry.Path]; ok {
			candidates = append(candidates, entry)
		}
	}
	j.mu.Unlock()

	drop := make(map[string]struct{}, len(candidates))
	for _, entry := range candidates {
		if snapshotUnchanged(entry) {
			drop[entry.Path] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	sj = j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		return
	}
	turn = sj.turns[len(sj.turns)-1]
	next := make([]FileChangeEntry, 0, len(turn.entries))
	for _, entry := range turn.entries {
		if _, ok := drop[entry.Path]; ok {
			continue
		}
		next = append(next, entry)
	}
	turn.entries = next
	turn.byPath = make(map[string]int, len(next))
	for i, entry := range next {
		turn.byPath[entry.Path] = i
	}
	previews := make([]TurnPreview, 0, len(turn.previews))
	for _, preview := range turn.previews {
		if _, ok := drop[preview.Path]; ok && preview.Tool == "exec_command" {
			continue
		}
		previews = append(previews, preview)
	}
	turn.previews = previews
}

func snapshotUnchanged(entry FileChangeEntry) bool {
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return !entry.Existed && len(entry.Before) == 0
		}
		return false
	}
	return entry.Existed && bytes.Equal(data, entry.Before)
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
	sj := j.sessions[sessionID]
	if sj == nil || len(sj.turns) == 0 {
		j.mu.Unlock()
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
		j.mu.Unlock()
		return FileChangeSummary{}
	}
	copied := append([]FileChangeEntry(nil), turn.entries...)
	started := turn.StartedAt
	j.mu.Unlock()

	out := make([]FileChangeEntry, len(copied))
	for i, e := range copied {
		after, err := os.ReadFile(e.Path)
		if err != nil {
			after = nil
		}
		added, deleted := lineChangeCounts(e.Before, after)
		out[i] = FileChangeEntry{
			Path: e.Path, Tool: e.Tool, Existed: e.Existed,
			Added: added, Deleted: deleted,
		}
	}
	return FileChangeSummary{
		Count:     len(out),
		StartedAt: started,
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

// lineChangeCounts reports added and deleted lines between two snapshots.
// Binary payloads (embedded NUL) report no line stats.
func lineChangeCounts(before, after []byte) (added, deleted int) {
	if bytes.IndexByte(before, 0) >= 0 || bytes.IndexByte(after, 0) >= 0 {
		return 0, 0
	}
	b := splitSnapshotLines(before)
	a := splitSnapshotLines(after)
	if len(b) == 0 {
		return len(a), 0
	}
	if len(a) == 0 {
		return 0, len(b)
	}
	const maxLines = 2000
	if len(b) > maxLines || len(a) > maxLines {
		return bagLineCounts(b, a)
	}
	common := lcsLineCount(b, a)
	return len(a) - common, len(b) - common
}

func splitSnapshotLines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	text := string(raw)
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
	}
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func bagLineCounts(before, after []string) (added, deleted int) {
	freq := make(map[string]int, len(before))
	for _, line := range before {
		freq[line]++
	}
	for _, line := range after {
		if freq[line] > 0 {
			freq[line]--
			continue
		}
		added++
	}
	for _, n := range freq {
		deleted += n
	}
	return added, deleted
}

func lcsLineCount(a, b []string) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for _, left := range a {
		cur[0] = 0
		for j, right := range b {
			if left == right {
				cur[j+1] = prev[j] + 1
				continue
			}
			if cur[j] > prev[j+1] {
				cur[j+1] = cur[j]
			} else {
				cur[j+1] = prev[j+1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
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
