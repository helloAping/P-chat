package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

// No-progress loop guard.
//
// The stuck-loop guard in agent.go only fires on identical FAILING tool calls.
// The 2026-08 recipes.js regression was a loop where every tool SUCCEEDED (a
// one-shot `node tmp-lines.js ...` that kept returning file slices) but the
// model never mutated anything and kept re-reading the same file. This guard
// catches that shape: consecutive rounds that only read files already read,
// with no mutation, accumulate a streak. At noProgressDirectiveAfter the loop
// injects a steering message; at noProgressForceStopAfter it forces a text-only
// final round so the turn cannot spin forever.
const (
	// noProgressDirectiveAfter is the streak at which the guard tells the LLM
	// to stop re-reading and act (or give a conclusion).
	noProgressDirectiveAfter = 6
	// noProgressForceStopAfter is the streak at which the guard forces a
	// text-only final round (tools dropped), mirroring the max-rounds path.
	noProgressForceStopAfter = 10
)

// mutatingTools are tool names whose presence in a round counts as observable
// progress — they change state or wait on the user. Any round containing one
// resets the no-progress streak.
var mutatingTools = map[string]bool{
	"write_file": true,
	"edit_file":  true,
	"todo_write": true,
	"question":   true,
	"task":       true,
}

// pathLikeTokenRe matches the file-path portion of a shell token: a run of
// path characters ending in a common code/data extension. The leftmost match
// is recovered (FindString), so a path embedded in a longer token — e.g.
// `fs.readFileSync('server/routes/recipes.js','utf8')` — still yields
// `server/routes/recipes.js`.
var pathLikeTokenRe = regexp.MustCompile(`[A-Za-z0-9_./\\-]+\.(?:js|jsx|ts|tsx|vue|json|sql|py|go|md|html|css|scss|yaml|yml|toml|sh|ps1|java|rb|php|c|cpp|h|rs|txt|log|xml|ini|cfg|env)`)

// roundReadTargets returns the normalized set of file paths a round's tool
// calls reference. read_file/grep/list_files expose a `path` argument;
// exec_command carries the path(s) inside the command string, so we scan for
// path-like tokens. Paths are normalized (forward slashes, lowercased, ./ and
// quoting stripped) so different spellings of the same file collapse — the
// alternation `21:40` vs `41:74` of the same file must count as one target.
func roundReadTargets(calls []nativeToolCall) []string {
	set := make(map[string]bool)
	for _, c := range calls {
		var obj struct {
			Path    string `json:"path"`
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(c.ArgsJSON), &obj)
		if obj.Path != "" {
			if n := normalizeReadPath(obj.Path); n != "" {
				set[n] = true
			}
		}
		if obj.Command != "" {
			for _, tok := range strings.Fields(obj.Command) {
				tok = strings.Trim(tok, `"'`)
				if strings.HasPrefix(tok, "-") {
					continue
				}
				if m := pathLikeTokenRe.FindString(tok); m != "" {
					if n := normalizeReadPath(m); n != "" {
						set[n] = true
					}
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func normalizeReadPath(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.TrimPrefix(s, "./")
	return strings.ToLower(s)
}

// noProgressGuard tracks consecutive rounds that only re-read files already
// read, with no mutation. observe returns the current streak (0 = reset).
type noProgressGuard struct {
	streak      int
	prevTargets map[string]bool
}

func (g *noProgressGuard) observe(calls []nativeToolCall) int {
	for _, c := range calls {
		if mutatingTools[c.Name] {
			g.reset()
			return 0
		}
	}
	if len(calls) == 0 {
		g.reset()
		return 0
	}
	targets := roundReadTargets(calls)
	if len(targets) == 0 {
		g.reset()
		return 0
	}
	targetSet := make(map[string]bool, len(targets))
	for _, t := range targets {
		targetSet[t] = true
	}
	if g.prevTargets != nil && hasTargetOverlap(g.prevTargets, targetSet) {
		g.streak++
	} else {
		// Reading a file not seen in the previous round is exploration, not a
		// stall — restart the count from this round.
		g.streak = 1
	}
	g.prevTargets = targetSet
	return g.streak
}

func (g *noProgressGuard) reset() {
	g.streak = 0
	g.prevTargets = nil
}

func hasTargetOverlap(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}
