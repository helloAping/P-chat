package agent

import (
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

// stubSandboxForConfirm is a sandboxForConfirm stub that
// returns a controllable decision for each check type. Tests
// use it to drive the confirmTargetFor path through every
// decision branch without spinning up a real *sandbox.Sandbox.
type stubSandboxForConfirm struct {
	execDecision  tool.SandboxDecision
	writeDecision tool.SandboxDecision
	readDecision  tool.SandboxDecision
	execPattern   string
}

func (s *stubSandboxForConfirm) CheckExecDecision(_ string) tool.SandboxDecision {
	return s.execDecision
}
func (s *stubSandboxForConfirm) CheckWriteDecision(_, _ string) tool.SandboxDecision {
	return s.writeDecision
}
func (s *stubSandboxForConfirm) CheckReadDecision(_, _ string) tool.SandboxDecision {
	return s.readDecision
}
func (s *stubSandboxForConfirm) MatchedPattern(_ string) string { return s.execPattern }

// TestConfirmTargetFor_Table is the dispatch-table test for
// the 2026-07 refactor. Each row covers a different
// (tool_name, decision) combination and asserts the target
// struct the agent.go confirm branch receives.
func TestConfirmTargetFor_Table(t *testing.T) {
	const project = "D:\\projects\\myapp"

	cases := []struct {
		desc         string
		toolName     string
		args         string
		sb           *stubSandboxForConfirm
		wantOk       bool
		wantDecision tool.SandboxDecision
		wantClass    string
		wantRisk     string
		wantHasPath  bool
	}{
		{
			desc:     "exec_command: dangerous pattern → confirm",
			toolName: "exec_command",
			args:     `{"command":"rm -rf /"}`,
			sb:       &stubSandboxForConfirm{execDecision: tool.SandboxConfirm, execPattern: `\brm\b`},
			wantOk:   true, wantDecision: tool.SandboxConfirm,
			wantRisk: "high",
		},
		{
			desc:     "exec_command: benign command → allow",
			toolName: "exec_command",
			args:     `{"command":"ls -la"}`,
			sb:       &stubSandboxForConfirm{execDecision: tool.SandboxAllow},
			wantOk:   true, wantDecision: tool.SandboxAllow,
			wantRisk: "high",
		},
		{
			desc:     "write_file: project path → confirm",
			toolName: "write_file",
			args:     `{"path":"src/foo.go","content":"x"}`,
			sb:       &stubSandboxForConfirm{writeDecision: tool.SandboxConfirm},
			wantOk:   true, wantDecision: tool.SandboxConfirm,
			wantClass: "project", wantRisk: "high", wantHasPath: true,
		},
		{
			desc:     "read_file: project path → allow",
			toolName: "read_file",
			args:     `{"path":"src/foo.go"}`,
			sb:       &stubSandboxForConfirm{readDecision: tool.SandboxAllow},
			wantOk:   true, wantDecision: tool.SandboxAllow,
			wantClass: "project", wantRisk: "low", wantHasPath: true,
		},
		{
			desc:     "read_file: outside project → confirm",
			toolName: "read_file",
			args:     `{"path":"D:/etc/passwd"}`,
			sb:       &stubSandboxForConfirm{readDecision: tool.SandboxConfirm},
			wantOk:   true, wantDecision: tool.SandboxConfirm,
			wantClass: "global", wantRisk: "low", wantHasPath: true,
		},
		{
			desc:     "list_files: empty path defaults to project root",
			toolName: "list_files",
			args:     `{"path":""}`,
			sb:       &stubSandboxForConfirm{readDecision: tool.SandboxAllow},
			wantOk:   true, wantDecision: tool.SandboxAllow,
			wantClass: "project", wantRisk: "low", wantHasPath: true,
		},
		{
			desc:     "list_files: missing path defaults to project root",
			toolName: "list_files",
			args:     `{}`,
			sb:       &stubSandboxForConfirm{readDecision: tool.SandboxAllow},
			wantOk:   true, wantDecision: tool.SandboxAllow,
			wantClass: "project", wantRisk: "low", wantHasPath: true,
		},
		{
			desc:     "read_docx: same dispatch as read_file",
			toolName: "read_docx",
			args:     `{"path":"notes.docx"}`,
			sb:       &stubSandboxForConfirm{readDecision: tool.SandboxAllow},
			wantOk:   true, wantDecision: tool.SandboxAllow,
			wantRisk: "low", wantHasPath: true,
		},
		{
			desc:     "question: not covered by sandbox",
			toolName: "question",
			args:     `{"questions":[]}`,
			sb:       &stubSandboxForConfirm{},
			wantOk:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got, ok := confirmTargetFor(c.toolName, c.args, project, c.sb)
			if ok != c.wantOk {
				t.Fatalf("ok = %v, want %v", ok, c.wantOk)
			}
			if !ok {
				return
			}
			if int(got.Decision) != int(c.wantDecision) {
				t.Errorf("Decision = %d, want %d", got.Decision, c.wantDecision)
			}
			if c.wantClass != "" && got.PathClass != c.wantClass {
				t.Errorf("PathClass = %q, want %q", got.PathClass, c.wantClass)
			}
			if got.RiskLevel != c.wantRisk {
				t.Errorf("RiskLevel = %q, want %q", got.RiskLevel, c.wantRisk)
			}
			if c.wantHasPath && got.ResolvedPath == "" {
				t.Errorf("ResolvedPath is empty, want non-empty")
			}
		})
	}
}

// TestConfirmTargetFor_ExecWorkDirEscape is the P0 2026-07
// regression: an LLM can pass exec_command.work_dir to
// redirect the command to a path outside the project. The
// helper must bump the decision to Confirm when the work_dir
// is an absolute path outside projectRoot — even when the
// command itself doesn't match any dangerous pattern.
func TestConfirmTargetFor_ExecWorkDirEscape(t *testing.T) {
	const project = "D:\\projects\\myapp"
	sb := &stubSandboxForConfirm{execDecision: tool.SandboxAllow} // benign command
	got, ok := confirmTargetFor("exec_command",
		`{"command":"ls","work_dir":"D:/Windows/System32"}`, project, sb)
	if !ok {
		t.Fatal("expected confirmTargetFor to handle exec_command")
	}
	if got.Decision != tool.SandboxConfirm {
		t.Errorf("work_dir outside project should bump to Confirm, got %d", got.Decision)
	}
	if got.Reason == "" {
		t.Error("expected a non-empty Reason explaining the work_dir escape")
	}
}

// TestConfirmTargetFor_ExecWorkDirInsideProject_NoBump is the
// happy path: a work_dir that sits INSIDE the project root
// should NOT bump the decision to Confirm.
func TestConfirmTargetFor_ExecWorkDirInsideProject_NoBump(t *testing.T) {
	const project = "D:\\projects\\myapp"
	sb := &stubSandboxForConfirm{execDecision: tool.SandboxAllow}
	got, ok := confirmTargetFor("exec_command",
		`{"command":"ls","work_dir":"D:/projects/myapp/src"}`, project, sb)
	if !ok {
		t.Fatal("expected confirmTargetFor to handle exec_command")
	}
	if got.Decision != tool.SandboxAllow {
		t.Errorf("work_dir inside project should preserve Allow, got %d", got.Decision)
	}
}
