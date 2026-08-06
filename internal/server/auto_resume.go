package server

// auto_resume.go — T3 (P1): auto-resume no-progress breaker.
//
// 背景：2026-08-05 新增的"todo 未完成自动续跑"在 my-blog 会话被死循环
// 放大：LLM 无法完成任务 → 每轮 turn 结束 → todo 未完成 → 自动续跑 →
// 新 turn 又失败 → 再续跑（seq 4852/4943）。续跑后 todo 状态无任何变化
// （无 done / 无新 todo_write），属于无效续跑，应熔断。
//
// 机制：server 为每个 session 维护一个 resumeTracker，记录每次自动续跑
// 时的未完成 todo ID 快照。续跑前后快照无变化（未完成项未减少）则累计
// "无效续跑计数"，达到 maxNoProgressResumes 后停止自动续跑，转为正常
// error+done 并提示用户手动介入。用户新发消息（非自动续跑）删除 tracker，
// 计数清零。

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/p-chat/pchat/internal/tool"
)

// maxNoProgressResumes is how many consecutive auto-resumes with an
// UNCHANGED unfinished-todo set are allowed before the server stops
// resuming and asks the user to intervene. The first auto-resume after a
// user message only establishes the baseline; the following unchanged
// resumes count against the limit. 3 gives the LLM two chances to make
// todo progress after the baseline before the chain is cut.
const maxNoProgressResumes = 3

// resumeTracker tracks todo progress across the auto-resumes of one
// session. Guarded by its own mutex — SendMessage turns of the same
// session are serialised by the session lock, but different sessions run
// concurrently and the map is shared.
type resumeTracker struct {
	mu              sync.Mutex
	lastSnapshot    string // pending-todo id fingerprint of the last resume
	noProgressCount int    // consecutive resumes with an unchanged snapshot
}

// allow decides whether one more auto-resume may run for the session.
// snapshot is the current unfinished-todo fingerprint (see
// pendingTodoSnapshot). It returns (true, "") to resume, or (false,
// notice) to stop — notice tells the user the chain was cut and to
// intervene manually. A changed snapshot (an item was done / cancelled /
// a new one added) counts as progress and resets the counter.
func (t *resumeTracker) allow(snapshot string) (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if snapshot != t.lastSnapshot {
		// Progress: the unfinished-todo set changed since the last
		// resume (or this is the first resume establishing the baseline).
		t.lastSnapshot = snapshot
		t.noProgressCount = 0
		return true, ""
	}
	t.noProgressCount++
	if t.noProgressCount >= maxNoProgressResumes {
		// Give up on this chain: reset so a later manual message starts
		// fresh, and let the caller emit a terminal frame instead.
		t.noProgressCount = 0
		t.lastSnapshot = ""
		return false, fmt.Sprintf("自动续跑已连续 %d 次且 todo 无进展，已停止自动续跑。请检查任务状态后手动继续。", maxNoProgressResumes)
	}
	return true, ""
}

// pendingTodoSnapshot returns the sorted, comma-joined ids of the
// session's unfinished todos (pending / in_progress) — the progress
// fingerprint compared across auto-resumes. Sorted ids beat a bare count:
// one item cancelled while another is added keeps the count the same but
// IS progress, and the ids reflect that.
func pendingTodoSnapshot(sessionID string) string {
	all := tool.GetSessionTodos(sessionID)
	ids := make([]string, 0, len(all))
	for _, t := range all {
		if t.Status == "pending" || t.Status == "in_progress" {
			ids = append(ids, t.ID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// shouldAutoResume decides whether one more auto-resume may run for the
// session. Returns (false, notice) when the no-progress limit has been
// hit; the caller then emits the terminal frame plus the notice instead
// of re-running the turn.
func (h *Handler) shouldAutoResume(sessionID string) (bool, string) {
	if sessionID == "" {
		return true, ""
	}
	v, _ := h.resumeTrackers.LoadOrStore(sessionID, &resumeTracker{})
	return v.(*resumeTracker).allow(pendingTodoSnapshot(sessionID))
}
