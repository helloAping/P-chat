package agent

// breaker.go — T4 (P1): cross-turn tool-failure breaker.
//
// 背景：my-blog 会话（2026-08-05）同一 turn 内工具失败 810 次未被熔断
// 拦住。same-tool（同命令连错 4 次）和 CumToolErrMax（累计 8 次）的计数
// 是 ChatWithTools 的局部变量，每次 turn 新建 → 被 turn 边界重置。LLM
// 每轮新 turn（自动续跑）又重新失败循环。
//
// 修复：把熔断计数提升为 Agent 级、按 session 隔离的累计状态
// （breakerState，Agent.breakers sync.Map）。自动续跑 turn 不清零、持续
// 累计；用户新发消息（新意图）清零恢复；同一"失败命令签名"（命令名 +
// 归一化参数）跨 turn 连续失败达到 crossTurnSameToolMax 即注入"不要重试，
// 换方式"，累计失败达到 CumToolErrMax 强制转总结。

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// crossTurnSameToolMax is how many consecutive same-signature tool
// failures (across the turns of one session) trip the cross-turn breaker.
// Mirrors the intra-turn sameToolErrMax; the cross-turn version keys on
// the normalized command signature instead of the bare tool name, so
// variants like `go test ... > test_out3.txt` vs `... > test_all.txt`
// count as the same failing command.
const crossTurnSameToolMax = 4

// breakerState accumulates tool-failure signatures across turns of one
// session. Guarded by its own mutex: the server serialises turns per
// session (session lock), but the CLI and the server can share a process
// and different sessions run concurrently — never rely on that.
type breakerState struct {
	mu     sync.Mutex
	sig    string // normalized signature of the last failing tool call
	streak int    // consecutive same-signature failures across turns
	cum    int    // total tool failures since the last reset
}

// reset clears the whole state. Called on user-initiated turns (new
// intent → fresh budget), on intra-turn breaker fires (avoid double
// messages), and after the cross-turn breaker fires.
func (b *breakerState) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sig = ""
	b.streak = 0
	b.cum = 0
}

// resetStreak clears only the same-signature streak, keeping the
// cumulative count. Called after a round that produced no tool failures —
// progress was made, so a failing streak should not carry into later
// rounds/turns; the cumulative count still reflects how much has failed.
func (b *breakerState) resetStreak() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sig = ""
	b.streak = 0
}

// recordFailure feeds one failing tool call into the state. Returns the
// updated (streak, cum) for the caller's bookkeeping. A failing signature
// that matches the previous one extends the streak; anything else starts
// a new streak (whack-a-mole commands never accumulate a same-tool
// streak, which is exactly why the cumulative guard exists).
func (b *breakerState) recordFailure(sig string) (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cum++
	if sig != "" && sig == b.sig {
		b.streak++
	} else {
		b.sig = sig
		b.streak = 1
	}
	return b.streak, b.cum
}

// peek returns the current (streak, cum, sig) without mutating. The sig
// is needed by the caller to name the failing command in the injected
// message before the breaker is reset.
func (b *breakerState) peek() (int, int, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.streak, b.cum, b.sig
}

// isAutoResumeTurn reports whether the request is a server-injected
// auto-resume (ClientMsgID==0 + TodoMode=resume) rather than a real user
// message. Auto-resume turns must NOT reset the cross-turn breaker — the
// whole point is to accumulate across the resume chain (the my-blog
// dead-loop). A genuine user message resets it: the user changed intent,
// so the breaker starts fresh (the T4 "熔断后能恢复" requirement).
func isAutoResumeTurn(req ChatRequest) bool {
	return req.ClientMsgID == 0 && todoModeFromRequest(req.TodoMode) == TodoModeResume
}

// toolFailureSigRe strips the volatile parts of a shell command for
// signature matching: the `-run` test filter (with quoted or unquoted
// argument), quoted segments (test names, findstr patterns), digits,
// `> file` redirects and `& type file` tails. Kept as package vars
// (compiled once) rather than per-call for the hot tool path.
var (
	toolFailureRunRe   = regexp.MustCompile(`-run\s+(?:"[^"]*"|'[^']*'|\S+)`)
	toolFailureQuoteRe = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	toolFailureDigitRe = regexp.MustCompile(`\d+`)
	toolFailureRedirRe = regexp.MustCompile(`>\s*\S+`)
	toolFailureTypeRe  = regexp.MustCompile(`&\s*type\s+\S+`)
)

// normalizeToolFailureSig produces a stable signature for a failing tool
// call so that "the same command with slightly different arguments" is
// recognised as the same failure. Without this, the LLM's habit of
// varying a filename or test filter every attempt
// (`test_all.txt` → `test_out3.txt`) would bypass the same-tool breaker —
// the 2026-08 my-blog session did exactly this for 810 failures.
//
// Only exec_command gets the loose normalization; other tools fall back
// to name + exact args (their arguments are already small and stable).
func normalizeToolFailureSig(name, argsJSON string) string {
	if name != "exec_command" {
		return name + "|" + argsJSON
	}
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil || args.Command == "" {
		return name + "|" + argsJSON
	}
	s := strings.ToLower(args.Command)
	s = toolFailureRunRe.ReplaceAllString(s, "")
	s = toolFailureQuoteRe.ReplaceAllString(s, "")
	s = toolFailureTypeRe.ReplaceAllString(s, "")
	s = toolFailureRedirRe.ReplaceAllString(s, "")
	s = toolFailureDigitRe.ReplaceAllString(s, "")
	// Strip pipeline tails (`| findstr ...`, `| grep ...`): the display
	// filter varies with the LLM's mood but the underlying command does
	// not — `go test ./internal/service/ -v` with or without a findstr
	// pipe must count as the same failing command.
	if i := strings.IndexByte(s, '|'); i >= 0 {
		s = s[:i]
	}
	// Drop a bare "-run" that lost its argument to the regexes above.
	fields := strings.Fields(s)
	kept := fields[:0]
	for _, f := range fields {
		if f != "-run" {
			kept = append(kept, f)
		}
	}
	return name + "|" + strings.Join(kept, " ")
}

// toolNameFromSig extracts the tool name from a normalized signature
// ("exec_command|go test ..." → "exec_command"), for user-facing
// messages where the full command is too long.
func toolNameFromSig(sig string) string {
	if i := strings.IndexByte(sig, '|'); i > 0 {
		return sig[:i]
	}
	return sig
}

// ClearBreakerState drops the cross-turn breaker entry for a session.
// Called by the server when a SendMessage retry chain fully ends (the
// auto-resume loop exits), so dead sessions do not accumulate
// breakerState entries for the process lifetime. It must NOT be called
// between the turns of one chain — the accumulated streak is exactly what
// the next auto-resume needs (T4). A missing entry is a no-op: the next
// user turn's LoadOrStore recreates a fresh state.
func (a *Agent) ClearBreakerState(sessionID string) {
	if sessionID == "" {
		return
	}
	a.breakers.Delete(sessionID)
}
