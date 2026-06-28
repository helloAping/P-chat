package memory

import (
	"context"
	"fmt"
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
	if len(ids) <= sm.triggerAt {
		return false, "", nil
	}

	summarized := map[int64]bool{}
	srows, _ := sm.store.db.Query(
		`SELECT range_start, range_end FROM summaries WHERE conversation_id = ?`,
		convID,
	)
	if srows != nil {
		defer srows.Close()
		for srows.Next() {
			var s, e int64
			if err := srows.Scan(&s, &e); err == nil {
				for i := s; i <= e; i++ {
					summarized[i] = true
				}
			}
		}
	}

	toSummarize := []int64{}
	for _, id := range ids {
		if !summarized[id] {
			toSummarize = append(toSummarize, id)
		}
	}
	if len(toSummarize) < 4 {
		return false, "", nil
	}
	half := len(toSummarize) / 2
	if half > 20 {
		half = 20
	}
	rangeIDs := toSummarize[:half]
	startID, endID := rangeIDs[0], rangeIDs[len(rangeIDs)-1]

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
		return false, "", err
	}
	if err := sm.store.SaveSummary(convID, startID, endID, summary); err != nil {
		return false, summary, err
	}
	return true, summary, nil
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

	// Find already-summarized ids.
	summarized := map[int64]bool{}
	srows, _ := sm.store.db.Query(
		`SELECT range_start, range_end FROM summaries WHERE conversation_id = ?`,
		convID,
	)
	if srows != nil {
		defer srows.Close()
		for srows.Next() {
			var s, e int64
			if err := srows.Scan(&s, &e); err == nil {
				for i := s; i <= e; i++ {
					summarized[i] = true
				}
			}
		}
	}

	// Pick the oldest non-summarized block (up to half of the message list).
	toSummarize := []int64{}
	for _, id := range ids {
		if !summarized[id] {
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
