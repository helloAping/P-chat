package agent

import (
	"testing"
)

// roundReadTargets must extract the file paths a round's tool calls actually
// touch, so the no-progress guard can tell "re-reading the same file" (stall)
// from "walking through new files" (progress). The regression shape is the
// agent calling `node tmp-lines.js server/routes/recipes.js 21:40` over and
// over — the path to the file being read must survive extraction.
func TestRoundReadTargets_ExtractsFilePaths(t *testing.T) {
	calls := []nativeToolCall{
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/recipes.js"}`},
		{Name: "grep", ArgsJSON: `{"path":"client/src","pattern":"router"}`},
		{Name: "exec_command", ArgsJSON: `{"command":"node tmp-lines.js server/routes/recipes.js 21:40"}`},
	}
	got := roundReadTargets(calls)
	want := map[string]bool{
		"server/routes/recipes.js": true,
		"client/src":               true,
		"tmp-lines.js":             true,
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("roundReadTargets returned unexpected path %q (all: %v)", g, got)
		}
	}
	for w := range want {
		if !hasStr(got, w) {
			t.Errorf("roundReadTargets missing expected path %q (all: %v)", w, got)
		}
	}
	// No tool calls / no path args → empty.
	if got := roundReadTargets(nil); len(got) != 0 {
		t.Errorf("roundReadTargets(nil) = %v, want empty", got)
	}
	if got := roundReadTargets([]nativeToolCall{{Name: "question", ArgsJSON: `{}`}}); len(got) != 0 {
		t.Errorf("roundReadTargets(question) = %v, want empty", got)
	}
}

// A `node -e` inline command carries the path inside a quoted JS string; the
// read target must still be recovered (the bug loop used both forms).
func TestRoundReadTargets_InlineNodeCommand(t *testing.T) {
	calls := []nativeToolCall{{
		Name:     "exec_command",
		ArgsJSON: `{"command":"node -e \"const fs=require('fs');const c=fs.readFileSync('server/routes/recipes.js','utf8');console.log(c)\""}`,
	}}
	got := roundReadTargets(calls)
	if !hasStr(got, "server/routes/recipes.js") {
		t.Errorf("inline node command path not recovered; got %v", got)
	}
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestNoProgressGuard_StallsOnRepeatedSameFileReads is the core no-progress
// regression test: consecutive rounds that only re-read the same file (the
// 2026-08 recipes.js loop, alternating 21:40 / 41:74 ranges of the same path)
// must accumulate a stall streak and eventually trip the guard.
func TestNoProgressGuard_StallsOnRepeatedSameFileReads(t *testing.T) {
	var g noProgressGuard
	readRecipes := []nativeToolCall{{
		Name:     "exec_command",
		ArgsJSON: `{"command":"node tmp-lines.js server/routes/recipes.js 21:40"}`,
	}}
	readRecipes2 := []nativeToolCall{{
		Name:     "exec_command",
		ArgsJSON: `{"command":"node tmp-lines.js server/routes/recipes.js 41:74"}`,
	}}
	// Alternating ranges of the SAME file are still the same read target.
	first := g.observe(readRecipes)
	if first != 1 {
		t.Fatalf("first observe streak = %d, want 1", first)
	}
	got := 1
	for i := 0; i < 20; i++ {
		s := g.observe(readRecipes2)
		if s > got {
			got = s
		}
		s = g.observe(readRecipes)
		if s > got {
			got = s
		}
	}
	if got < noProgressForceStopAfter {
		t.Errorf("stall streak only reached %d after 40 same-file read rounds, want >= %d", got, noProgressForceStopAfter)
	}
}

// A round containing any mutating tool (write/edit/todo/question) is progress
// and must reset the stall streak.
func TestNoProgressGuard_MutationResets(t *testing.T) {
	var g noProgressGuard
	readRecipes := []nativeToolCall{{
		Name:     "exec_command",
		ArgsJSON: `{"command":"node tmp-lines.js server/routes/recipes.js 21:40"}`,
	}}
	if s := g.observe(readRecipes); s != 1 {
		t.Fatalf("initial streak = %d, want 1", s)
	}
	mutating := []nativeToolCall{{
		Name:     "edit_file",
		ArgsJSON: `{"path":"server/routes/recipes.js","old_text":"a","new_text":"b"}`,
	}}
	if s := g.observe(mutating); s != 0 {
		t.Errorf("streak after mutation = %d, want reset to 0", s)
	}
	if s := g.observe(readRecipes); s != 1 {
		t.Errorf("streak after reset + new read = %d, want 1", s)
	}
}

// Moving on to different files is legitimate exploration, not a stall.
func TestNoProgressGuard_NewFilesDoNotAccumulate(t *testing.T) {
	var g noProgressGuard
	reads := []nativeToolCall{
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/a.js"}`},
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/b.js"}`},
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/c.js"}`},
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/d.js"}`},
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/e.js"}`},
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/f.js"}`},
		{Name: "read_file", ArgsJSON: `{"path":"server/routes/g.js"}`},
	}
	for _, r := range reads {
		if s := g.observe([]nativeToolCall{r}); s > 1 {
			t.Fatalf("walking through distinct files accumulated streak %d", s)
		}
	}
}

// TestNoProgressConstants pins the thresholds so a future change updates the
// plan docs, matching the codebase convention for loop-guard constants.
func TestNoProgressConstants(t *testing.T) {
	if noProgressDirectiveAfter >= noProgressForceStopAfter {
		t.Errorf("noProgressDirectiveAfter (%d) must be < noProgressForceStopAfter (%d)", noProgressDirectiveAfter, noProgressForceStopAfter)
	}
	if noProgressForceStopAfter < 3 {
		t.Errorf("noProgressForceStopAfter (%d) too low — would stall legitimate multi-read rounds", noProgressForceStopAfter)
	}
	if noProgressForceStopAfter > 30 {
		t.Errorf("noProgressForceStopAfter (%d) too high — lets a runaway loop run too long", noProgressForceStopAfter)
	}
}
