package dynamic

import (
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

func TestLookupSpecForRoot_ProjectOverridesGlobal(t *testing.T) {
	SetSpecs(nil)
	SetSpecsForRoot(tool.ToolOriginProject, "C:/repo/a", nil)
	t.Cleanup(func() {
		SetSpecs(nil)
		SetSpecsForRoot(tool.ToolOriginProject, "C:/repo/a", nil)
	})
	SetSpecs(map[string]Spec{
		"shared": {Name: "shared", Description: "global", Sandbox: SandboxConfig{Exec: "confirm"}},
	})
	SetSpecsForRoot(tool.ToolOriginProject, "C:/repo/a", map[string]Spec{
		"shared": {Name: "shared", Description: "project", Sandbox: SandboxConfig{Exec: "allow"}},
	})

	global, ok := LookupSpecForRoot("shared", "")
	if !ok || global.Description != "global" || global.Sandbox.Exec != "confirm" {
		t.Fatalf("global spec = %+v ok=%v, want global confirm", global, ok)
	}
	project, ok := LookupSpecForRoot("shared", "C:/repo/a")
	if !ok || project.Description != "project" || project.Sandbox.Exec != "allow" {
		t.Fatalf("project spec = %+v ok=%v, want project allow", project, ok)
	}
}

func TestDiagnosticsSnapshotForRoot_IsolatesProjects(t *testing.T) {
	SetDiagnostics(nil)
	SetDiagnosticsForRoot(tool.ToolOriginProject, "C:/repo/a", nil)
	SetDiagnosticsForRoot(tool.ToolOriginProject, "C:/repo/b", nil)
	t.Cleanup(func() {
		SetDiagnostics(nil)
		SetDiagnosticsForRoot(tool.ToolOriginProject, "C:/repo/a", nil)
		SetDiagnosticsForRoot(tool.ToolOriginProject, "C:/repo/b", nil)
	})
	SetDiagnostics(map[string]LoadDiagnostic{
		"global.yaml": {Source: "global.yaml", Status: "loaded"},
	})
	SetDiagnosticsForRoot(tool.ToolOriginProject, "C:/repo/a", map[string]LoadDiagnostic{
		"a.yaml": {Source: "a.yaml", Status: "error", Error: "bad"},
	})
	SetDiagnosticsForRoot(tool.ToolOriginProject, "C:/repo/b", map[string]LoadDiagnostic{
		"b.yaml": {Source: "b.yaml", Status: "error", Error: "bad"},
	})

	got := DiagnosticsSnapshotForRoot("C:/repo/a")
	seen := map[string]bool{}
	for _, d := range got {
		seen[d.Source] = true
	}
	if !seen["global.yaml"] || !seen["a.yaml"] {
		t.Fatalf("diagnostics = %+v, want global+a", got)
	}
	if seen["b.yaml"] {
		t.Fatalf("diagnostics = %+v, leaked project b", got)
	}
}
