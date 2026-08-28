package agent

import (
	"strings"
	"testing"
)

func TestExecOperationFingerprint_GoTestDisplayVariantsMatch(t *testing.T) {
	calls := []nativeToolCall{
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-Object -Last 20; Write-Host 'exit:' $LASTEXITCODE\""}`},
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-String 'FAIL\\|ok' | ForEach-Object { $_.Line.Trim() }; Write-Host 'exit:' $LASTEXITCODE\""}`},
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-Object -Last 15\""}`},
	}
	first, ok := execOperationFingerprint(calls[0])
	if !ok {
		t.Fatal("first go test command did not produce a fingerprint")
	}
	for i, call := range calls[1:] {
		got, ok := execOperationFingerprint(call)
		if !ok {
			t.Fatalf("variant %d did not produce a fingerprint", i+1)
		}
		if got != first {
			t.Fatalf("variant %d fingerprint = %q, want %q", i+1, got, first)
		}
	}
}

func TestExecOperationFingerprint_GetContentVariantsMatch(t *testing.T) {
	a := nativeToolCall{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"Get-Content -Encoding UTF8 internal\\agent\\agent.go\""}`}
	b := nativeToolCall{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"$c=Get-Content -Encoding UTF8 internal\\agent\\agent.go; $c[38..65]\""}`}
	afp, ok := execOperationFingerprint(a)
	if !ok {
		t.Fatal("Get-Content command did not produce a fingerprint")
	}
	bfp, ok := execOperationFingerprint(b)
	if !ok {
		t.Fatal("Get-Content slice command did not produce a fingerprint")
	}
	if afp != bfp {
		t.Fatalf("fingerprints differ: %q vs %q", afp, bfp)
	}
}

func TestExecOperationFingerprint_SelectStringKeepsPattern(t *testing.T) {
	a := nativeToolCall{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"Select-String -Path 'web\\admin\\login.html' -Pattern 'captcha','验证码'\""}`}
	b := nativeToolCall{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"Select-String -Path 'web\\admin\\login.html' -Pattern 'site_url','站点'\""}`}
	afp, ok := execOperationFingerprint(a)
	if !ok {
		t.Fatal("Select-String command did not produce a fingerprint")
	}
	bfp, ok := execOperationFingerprint(b)
	if !ok {
		t.Fatal("second Select-String command did not produce a fingerprint")
	}
	if afp == bfp {
		t.Fatalf("different search patterns must stay distinct: %q", afp)
	}
}

func TestBuildSubagentToolCompressionPlan_OnlyChildChatsCompress(t *testing.T) {
	calls := []nativeToolCall{
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-Object -Last 20\""}`},
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-Object -Last 15\""}`},
	}
	if got := buildSubagentToolCompressionPlan(ChatRequest{}, calls); len(got) != 0 {
		t.Fatalf("parent chat compression plan = %#v, want none", got)
	}
	got := buildSubagentToolCompressionPlan(ChatRequest{SubagentType: "explore"}, calls)
	if len(got) != 1 {
		t.Fatalf("subagent compression entries = %d, want 1 (%#v)", len(got), got)
	}
	compressed, ok := got[1]
	if !ok {
		t.Fatalf("second call was not compressed: %#v", got)
	}
	if compressed.coveredBy != 0 {
		t.Fatalf("coveredBy = %d, want 0", compressed.coveredBy)
	}
	res := compressedToolResult(compressed)
	if res.IsError {
		t.Fatal("compressed result must be successful so the LLM can continue from prior evidence")
	}
	if !strings.Contains(res.Content, "covered by tool call 1") {
		t.Fatalf("compressed result does not explain coverage: %q", res.Content)
	}
}

func TestBuildSubagentToolCompressionPlan_PrefersLessFilteredRepresentative(t *testing.T) {
	calls := []nativeToolCall{
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-String 'FAIL'\""}`},
		{Name: "exec_command", ArgsJSON: `{"command":"chcp 65001 >nul & powershell -Command \"go test ./... 2>&1 | Select-Object -Last 20\""}`},
	}
	got := buildSubagentToolCompressionPlan(ChatRequest{SubagentType: "explore"}, calls)
	if len(got) != 1 {
		t.Fatalf("compression entries = %d, want 1 (%#v)", len(got), got)
	}
	compressed, ok := got[0]
	if !ok {
		t.Fatalf("more-filtered first call should be compressed by the less-filtered second call: %#v", got)
	}
	if compressed.coveredBy != 1 {
		t.Fatalf("coveredBy = %d, want 1", compressed.coveredBy)
	}
}
