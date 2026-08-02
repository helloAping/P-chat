package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/p-chat/pchat/internal/llm"
)

// Summarizer compresses long conversation history by asking an LLM to
// summarize the oldest messages. The summary is stored in the
// `summaries` table and prepended to GetMessages() when applicable.
type Summarizer struct {
	store     *Store
	llm       *llm.Client
	provider  string
	triggerAt int // when total messages exceed this, summarize the oldest half
}

// maxCompressBatches caps how many batches Compress summarizes in a single
// invocation (each batch is up to 100 messages). A pathological conversation —
// e.g. the 2026-08 runaway loop that left 2000+ rows in one session — would
// otherwise trigger 20+ sequential synchronous LLM calls inside the
// SendMessage handler, blocking the turn and hammering the provider (the
// "发送继续后 server 卡顿/内存飙升" resume regression). Each call advances
// the compression point; later Compress invocations (from subsequent messages
// or in-loop auto-compact) cover the rest.
const maxCompressBatches = 4

func NewSummarizer(s *Store, l *llm.Client, provider string, triggerAt int) *Summarizer {
	if triggerAt <= 0 {
		triggerAt = 50
	}
	return &Summarizer{store: s, llm: l, provider: provider, triggerAt: triggerAt}
}

// Compress runs one pass of summarization on the oldest non-summarized
// messages. Returns whether anything was compressed and the summary text.
func (sm *Summarizer) Compress(ctx context.Context, convID string) (bool, string, error) {
	if sm == nil || sm.store == nil || sm.llm == nil {
		return false, "", nil
	}
	_ = sm.store.Flush()
	rows, err := sm.store.db.Query(
		`SELECT id FROM messages WHERE conversation_id = ? ORDER BY id ASC`,
		convID,
	)
	if err != nil {
		return false, "", err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, "", err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return false, "", nil
	}

	// Marks message ids covered by an existing summary. Ranges are kept as
	// (start, end) pairs and matched by binary search — expanding each range
	// into every integer would be O(span) memory, and a single summary range
	// spanning two message-id schemes (small autoincrement ids + millisecond
	// timestamps, e.g. the 2026-08 runaway conversation) has a span on the
	// order of 10^15, which blew the process heap to GBs inside the
	// SendMessage handler.
	summaryRanges := sm.loadSummaryRanges(convID)
	summarized := func(id int64) bool { return rangeContainsID(summaryRanges, id) }

	toSummarize := []int64{}
	for _, id := range ids {
		if !summarized(id) {
			toSummarize = append(toSummarize, id)
		}
	}
	if len(toSummarize) == 0 {
		return false, "", nil
	}
	// Summarize up to maxCompressBatches batches per call (each batch is one
	// LLM summarization call capped at ~200 chars per message × 100 messages
	// = ~20K chars prompt, safely within the summarizer model's context).
	// Bounding the batches per invocation keeps a single Compress from firing
	// 20+ sequential LLM calls on a bloated conversation; the compression
	// point advances incrementally and is picked up by the next call.
	var allSummaries []string
	batches := 0
	for len(toSummarize) > 0 && batches < maxCompressBatches {
		select {
		case <-ctx.Done():
			if len(allSummaries) == 0 {
				return false, "", ctx.Err()
			}
			return true, strings.Join(allSummaries, "\n"), ctx.Err()
		default:
		}
		batches++
		batch := toSummarize
		if len(batch) > 100 {
			batch = batch[:100]
		}
		startID, endID := batch[0], batch[len(batch)-1]

		texts := make([]string, 0, len(batch))
		for _, id := range batch {
			var role, content string
			if err := sm.store.db.QueryRow(
				`SELECT role, content FROM messages WHERE id = ?`, id,
			).Scan(&role, &content); err == nil {
				t := role + ": " + truncateStr(content, 200)
				texts = append(texts, t)
			}
		}
		joined := strings.Join(texts, "\n")

		summary, err := sm.summarize(ctx, joined)
		if err != nil {
			if len(allSummaries) == 0 {
				return false, "", err
			}
			return true, strings.Join(allSummaries, "\n"), err
		}
		if err := sm.store.SaveSummary(convID, startID, endID, summary); err != nil {
			if len(allSummaries) == 0 {
				return false, summary, err
			}
			return true, strings.Join(allSummaries, "\n"), err
		}
		allSummaries = append(allSummaries, summary)
		toSummarize = toSummarize[len(batch):]
	}
	return true, strings.Join(allSummaries, "\n"), nil
}

// MaybeSummarize checks if the current conversation has grown past the
// trigger threshold. If so, it summarizes the oldest half of messages
// (those that haven't been summarized yet) and stores the result.
func (sm *Summarizer) MaybeSummarize(ctx context.Context, convID string) (bool, error) {
	if sm == nil || sm.store == nil || sm.llm == nil {
		return false, nil
	}
	rows, err := sm.store.db.Query(
		`SELECT id FROM messages WHERE conversation_id = ? ORDER BY id ASC`,
		convID,
	)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		ids = append(ids, id)
	}
	if len(ids) <= sm.triggerAt {
		return false, nil
	}

	// See Compress for why summarized coverage is tracked as ranges with
	// binary search instead of an expanded id→bool map.
	summaryRanges := sm.loadSummaryRanges(convID)
	summarized := func(id int64) bool { return rangeContainsID(summaryRanges, id) }

	// Pick the oldest non-summarized block (up to half of the message list).
	toSummarize := []int64{}
	for _, id := range ids {
		if !summarized(id) {
			toSummarize = append(toSummarize, id)
		}
	}
	if len(toSummarize) < 4 {
		return false, nil
	}
	// Take the first half.
	half := len(toSummarize) / 2
	if half > 20 {
		half = 20
	}
	rangeIDs := toSummarize[:half]
	startID, endID := rangeIDs[0], rangeIDs[len(rangeIDs)-1]

	// Read the content of these messages.
	texts := make([]string, 0, len(rangeIDs))
	for _, id := range rangeIDs {
		var role, content string
		if err := sm.store.db.QueryRow(
			`SELECT role, content FROM messages WHERE id = ?`, id,
		).Scan(&role, &content); err == nil {
			t := role + ": " + truncateStr(content, 200)
			texts = append(texts, t)
		}
	}
	joined := strings.Join(texts, "\n")

	summary, err := sm.summarize(ctx, joined)
	if err != nil {
		return false, err
	}
	if err := sm.store.SaveSummary(convID, startID, endID, summary); err != nil {
		return false, err
	}
	return true, nil
}

func (sm *Summarizer) summarize(ctx context.Context, text string) (string, error) {
	prompt := fmt.Sprintf(
		"请用简洁的要点形式总结以下对话片段，保留关键信息（用户需求、决策、工具调用结果等）。" +
			"不要超过 200 字。\n\n---\n%s\n---", text,
	)
	resp, err := sm.llm.Chat(ctx, sm.provider, "", []llm.Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// summaryRange is an inclusive [start, end] message-id interval already
// covered by a stored summary.
type summaryRange struct{ start, end int64 }

// loadSummaryRanges returns the stored summary intervals for a conversation,
// sorted by start. The range values come straight from the `summaries` table
// (startID/endID of each compressed batch), so they may be huge when a batch
// crossed two message-id schemes — that is fine, we never expand them.
func (sm *Summarizer) loadSummaryRanges(convID string) []summaryRange {
	srows, err := sm.store.db.Query(
		`SELECT range_start, range_end FROM summaries WHERE conversation_id = ?`,
		convID,
	)
	if err != nil {
		return nil
	}
	defer srows.Close()
	var out []summaryRange
	for srows.Next() {
		var s, e int64
		if err := srows.Scan(&s, &e); err == nil && e >= s {
			out = append(out, summaryRange{start: s, end: e})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// rangeContainsID reports whether id falls inside any interval. Ranges are
// sorted by start, so a binary search finds the last range starting at or
// before id in O(log n), then checks the span in O(1) — never iterating over
// the numeric distance between start and end.
func rangeContainsID(ranges []summaryRange, id int64) bool {
	lo, hi := 0, len(ranges)
	for lo < hi {
		mid := (lo + hi) / 2
		if ranges[mid].start <= id {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return false
	}
	return ranges[lo-1].end >= id
}
