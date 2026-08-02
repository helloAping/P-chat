package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLooksPersistentCommand locks the background-vs-inline routing decision.
// The 2026-08 runaway-loop regression was caused by `node <helper> <args>`
// calls being auto-backgrounded, so the LLM never saw their stdout. Server
// entrypoints must still background; one-shot helpers must run inline.
func TestLooksPersistentCommand(t *testing.T) {
	background := []string{
		"node server.js",
		"node C:/demo/app.js",
		`node C:\demo\main.js`,
		"node index.js --port 3000",
		"java -jar backend.jar",
		"npm run dev",
		"npm start",
		"vite",
		"next dev",
		"python -m http.server 8000",
		"node --watch server.js",
		"nodemon app.js",
	}
	inline := []string{
		// The exact regression shape: a one-shot helper taking a file + range.
		"node tmp-lines.js server/routes/recipes.js 21:40",
		"node tmp-slice.js server/routes/recipes.js 0:1400",
		"node tmp-read.js",
		"node helper.js /some/path.txt",
		"node -e \"console.log(1)\"",
		"node --version",
		"node -v",
		"java -version",
		"python script.py",
		"echo hello",
		"dir",
	}
	for _, cmd := range background {
		if !looksPersistentCommand(cmd) {
			t.Errorf("looksPersistentCommand(%q) = false, want true (should background)", cmd)
		}
	}
	for _, cmd := range inline {
		if looksPersistentCommand(cmd) {
			t.Errorf("looksPersistentCommand(%q) = true, want false (should run inline)", cmd)
		}
	}
}

// TestExecCommand_NodeHelperScript_RunsInline is the end-to-end regression test
// for the 2026-08 "最新菜谱对话卡死 + OOM" bug. The agent's file-read helper
// calls were shaped like:
//
//	node tmp-lines.js server/routes/recipes.js 21:40
//
// Before the fix, looksPersistentCommand treated *any* command starting with
// "node " as a long-running background process, so exec_command routed these
// one-shot helpers through startManagedProcess and returned a `proc_...`
// process-metadata JSON instead of the script's stdout. The LLM never saw the
// file content, so it kept re-launching the helper in an endless loop (600+
// subprocesses, ~1000 full-context LLM requests → OOM).
//
// A node helper script with arguments must run inline and return its output.
func TestExecCommand_NodeHelperScript_RunsInline(t *testing.T) {
	dir := t.TempDir()
	data := filepath.ToSlash(filepath.Join(dir, "data.txt"))
	if err := os.WriteFile(data, []byte("hello-pchat-node-inline"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A helper that reads a file path from argv and prints a marker line —
	// the exact shape of the tmp-*.js helpers the agent was calling.
	helper := filepath.ToSlash(filepath.Join(dir, "helper.js"))
	js := `const fs=require('fs');const f=process.argv[2];const c=fs.readFileSync(f,'utf8');console.log('MARKER_LINE '+c.trim());`
	if err := os.WriteFile(helper, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := handleExecCommand(context.Background(),
		[]byte(`{"command":"node `+helper+` `+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("helper command errored: %s", res.Content)
	}
	if strings.Contains(res.Content, `"id": "proc_`) || strings.Contains(res.Content, `"command": "node`) {
		t.Fatalf("helper command was routed to a background process (got process metadata, want inline output):\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "MARKER_LINE hello-pchat-node-inline") {
		t.Errorf("inline output missing marker line; got:\n%s", res.Content)
	}
}

// TestPruneExitedProcesses_BoundsRegistry is the regression test for the
// 2026-08 OOM: the managed-process registry (`processes`) grew without bound
// because exited background processes were never evicted. pruneExitedProcesses
// must bound the registry at maxManagedProcesses, evicting only EXITED
// entries (oldest first) and never a running process — otherwise the agent's
// 600+ backgrounded helper processes would pin ~600 registry entries plus
// their output buffers forever.
func TestPruneExitedProcesses_BoundsRegistry(t *testing.T) {
	// Isolate the global registry for this test.
	processMu.Lock()
	saved := processes
	processes = map[string]*managedProcess{}
	processMu.Unlock()
	defer func() {
		processMu.Lock()
		processes = saved
		processMu.Unlock()
	}()

	// A running process must survive eviction even though it is the oldest.
	processes["proc_running_oldest"] = &managedProcess{
		id:        "proc_running_oldest",
		command:   "node server.js",
		startedAt: time.Now().Add(-time.Hour),
	}
	// Fill the registry with exited entries well beyond the cap.
	for i := 0; i < maxManagedProcesses+10; i++ {
		id := fmt.Sprintf("proc_exited_%d", i)
		processes[id] = &managedProcess{
			id:        id,
			command:   "echo x",
			startedAt: time.Now().Add(-time.Duration(i) * time.Second),
			exited:    true,
			exitText:  "exit 0",
		}
	}

	pruneExitedProcesses()

	processMu.Lock()
	defer processMu.Unlock()
	if got := len(processes); got > maxManagedProcesses {
		t.Errorf("registry size = %d after prune, want <= %d", got, maxManagedProcesses)
	}
	if _, ok := processes["proc_running_oldest"]; !ok {
		t.Error("running process was evicted by prune; running processes must never be evicted")
	}
}
