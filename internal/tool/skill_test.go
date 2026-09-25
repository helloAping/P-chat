package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/skill"
)

type capturingSkillManager struct {
	change skill.ChangeRequest
	result *skill.ChangeResult
}

func (m *capturingSkillManager) Catalog(context.Context, skill.CatalogQuery) (skill.Catalog, error) {
	return skill.Catalog{}, nil
}

func (m *capturingSkillManager) Load(context.Context, skill.LoadRequest) (skill.LoadedSkill, error) {
	return skill.LoadedSkill{}, nil
}

func (m *capturingSkillManager) Apply(_ context.Context, change skill.ChangeRequest) (skill.ChangeResult, error) {
	m.change = change
	if m.result != nil {
		return *m.result, nil
	}
	return skill.ChangeResult{Name: change.Name, Scope: change.Scope, Directory: "skills/" + change.Name, Ready: true}, nil
}

func writeToolTestSkill(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: " + name + "\ndescription: test skill\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("GUIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func callTool(t *testing.T, registry *Registry, name string, args any) *CallResult {
	t.Helper()
	handler, ok := registry.Get(name)
	if !ok {
		t.Fatalf("tool %q is not registered", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRegisterSkillToolsLoadsWithStructuredInvocation(t *testing.T) {
	managed := t.TempDir()
	writeToolTestSkill(t, managed, "docs-tool", "DOC INSTRUCTIONS")
	manager := skill.NewFSManager(skill.ManagerOptions{GlobalManagedDir: managed, UserStandardDir: filepath.Join(t.TempDir(), "standard")})
	registry := NewRegistry()
	RegisterSkillTools(registry, manager)

	result := callTool(t, registry, "skill", map[string]any{"action": "load", "name": "docs-tool"})
	if result.IsError || result.SkillInvocation == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.SkillInvocation.Name != "docs-tool" || result.SkillInvocation.Status != "ready" {
		t.Fatalf("invocation = %+v", result.SkillInvocation)
	}
	if !strings.Contains(result.Content, "DOC INSTRUCTIONS") {
		t.Fatalf("content = %q", result.Content)
	}

	resource := callTool(t, registry, "skill", map[string]any{"action": "read_resource", "name": "docs-tool", "resource": "references/guide.md"})
	if resource.Content != "GUIDE" || resource.SkillInvocation != nil {
		t.Fatalf("resource result = %+v", resource)
	}
}

func TestRegisterSkillToolsKeepsManagementInOneTool(t *testing.T) {
	managed := t.TempDir()
	sourceRoot := t.TempDir()
	source := writeToolTestSkill(t, sourceRoot, "imported", "IMPORTED")
	manager := skill.NewFSManager(skill.ManagerOptions{GlobalManagedDir: managed, UserStandardDir: filepath.Join(t.TempDir(), "standard")})
	registry := NewRegistry()
	RegisterSkillTools(registry, manager)

	result := callTool(t, registry, "skill_manage", map[string]any{
		"action": "import", "source_path": source, "scope": "global",
	})
	if result.IsError || len(result.ChangedPaths) != 1 || result.ChangedPaths[0] != filepath.Join(managed, "imported") {
		t.Fatalf("import result = %+v", result)
	}
	if _, ok := registry.Get("skill_install"); ok {
		t.Fatal("management operations must not be split into a third model-visible tool")
	}
}

func TestSkillManageImportsNamedSkillFromCollectionThroughManager(t *testing.T) {
	manager := &capturingSkillManager{}
	registry := NewRegistry()
	RegisterSkillTools(registry, manager)

	result := callTool(t, registry, "skill_manage", map[string]any{
		"action": "import", "source_path": "C:/exports/vendor-skills", "name": "docs-tool", "scope": "global",
	})
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}
	if manager.change.Action != skill.ChangeImport || manager.change.SourcePath != "C:/exports/vendor-skills" || manager.change.Name != "docs-tool" {
		t.Fatalf("change = %+v", manager.change)
	}
}

func TestSkillManageRequiresURLForInstall(t *testing.T) {
	manager := &capturingSkillManager{}
	registry := NewRegistry()
	RegisterSkillTools(registry, manager)

	result := callTool(t, registry, "skill_manage", map[string]any{
		"action": "install",
	})
	if !result.IsError || !strings.Contains(result.Content, "source_url is required") {
		t.Fatalf("result = %+v, want missing source URL error", result)
	}
	if manager.change.Action != "" {
		t.Fatalf("manager must not run without a source URL: %+v", manager.change)
	}
}

func TestSkillManageReportsCollectionImportRollback(t *testing.T) {
	manager := &capturingSkillManager{result: &skill.ChangeResult{
		Name: "docs-tool", Scope: skill.ScopeGlobalManaged, Directory: "skills", Ready: false, RolledBack: true,
	}}
	registry := NewRegistry()
	RegisterSkillTools(registry, manager)

	result := callTool(t, registry, "skill_manage", map[string]any{
		"action": "import", "source_path": "C:/exports/vendor-skills", "name": "docs-tool",
	})
	if !result.IsError || !strings.Contains(result.Summary, "rolled back") || len(result.ChangedPaths) != 0 {
		t.Fatalf("result = %+v, want explicit rollback without changed paths", result)
	}
}
