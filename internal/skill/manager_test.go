package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillPackage(t *testing.T, root, dirName, manifest string, resources map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range resources {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testManager(t *testing.T) (*FSManager, string, string) {
	t.Helper()
	base := t.TempDir()
	managed := filepath.Join(base, "managed")
	standard := filepath.Join(base, "standard")
	return NewFSManager(ManagerOptions{
		GlobalManagedDir: managed,
		UserStandardDir:  standard,
	}), managed, standard
}

func TestFSManagerCatalogUsesDeclaredNameAndSourcePrecedence(t *testing.T) {
	m, managed, standard := testManager(t)
	project := t.TempDir()

	writeSkillPackage(t, standard, "shared-dir", "---\nname: shared\ndescription: user standard\n---\nUSER STANDARD\n", nil)
	writeSkillPackage(t, managed, "shared", "---\nname: shared\ndescription: managed global\n---\nGLOBAL MANAGED\n", nil)
	writeSkillPackage(t, filepath.Join(project, ".agents", "skills"), "shared", "---\nname: shared\ndescription: project standard\n---\nPROJECT STANDARD\n", nil)
	winningDir := writeSkillPackage(t, filepath.Join(project, ".p-chat", "skills"), "different-dir", "---\nname: shared\ndescription: project managed\n---\nPROJECT MANAGED\n", nil)

	catalog, err := m.Catalog(context.Background(), CatalogQuery{ProjectRoot: project})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 1 {
		t.Fatalf("skills = %+v, want one effective skill", catalog.Skills)
	}
	got := catalog.Skills[0]
	if got.Name != "shared" || got.Description != "project managed" {
		t.Fatalf("effective skill = %+v", got)
	}
	if got.Scope != ScopeProjectManaged || got.Directory != winningDir {
		t.Fatalf("winning source = %+v, want %s", got, winningDir)
	}
	if len(got.Shadowed) != 3 {
		t.Fatalf("shadowed = %+v, want three lower priority sources", got.Shadowed)
	}
	foundMismatch := false
	for _, diag := range catalog.Diagnostics {
		if diag.Code == "directory_name_mismatch" && strings.Contains(diag.Message, "different-dir") {
			foundMismatch = true
		}
	}
	if !foundMismatch {
		t.Fatalf("diagnostics = %+v, want directory/frontmatter mismatch", catalog.Diagnostics)
	}
}

func TestFSManagerBuildIndexDoesNotInjectSkillBody(t *testing.T) {
	m, managed, _ := testManager(t)
	writeSkillPackage(t, managed, "alpha", "---\nname: alpha\ndescription: concise description\n---\nSECRET BODY MUST LOAD ON DEMAND\n", nil)

	catalog, err := m.Catalog(context.Background(), CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	index := BuildCatalogContext(catalog, 8*1024)
	if !strings.Contains(index, "alpha") || !strings.Contains(index, "concise description") {
		t.Fatalf("index missing discovery metadata: %q", index)
	}
	if strings.Contains(index, "SECRET BODY") {
		t.Fatalf("index leaked full skill instructions: %q", index)
	}
}

func TestFSManagerLoadIncludesDependenciesAndReadsContainedResource(t *testing.T) {
	m, managed, _ := testManager(t)
	writeSkillPackage(t, managed, "lark-shared", "---\nname: lark-shared\ndescription: shared helpers\n---\nSHARED INSTRUCTIONS\n", map[string]string{
		"references/auth.md": "AUTH REFERENCE",
	})
	writeSkillPackage(t, managed, "lark-doc", "---\nname: lark-doc\ndescription: manage docs\nmetadata:\n  requires:\n    skills: [lark-shared]\n    bins: [go]\n---\nDOC INSTRUCTIONS\n", map[string]string{
		"references/create.md": "CREATE REFERENCE",
	})

	loaded, err := m.Load(context.Background(), LoadRequest{Name: "lark-doc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Skills) != 2 || loaded.Skills[0].Name != "lark-shared" || loaded.Skills[1].Name != "lark-doc" {
		t.Fatalf("load order = %+v, want dependency before primary", loaded.Skills)
	}
	if !strings.Contains(loaded.Context, "SHARED INSTRUCTIONS") || !strings.Contains(loaded.Context, "DOC INSTRUCTIONS") {
		t.Fatalf("loaded context missing dependency or primary: %q", loaded.Context)
	}
	if len(loaded.RequiredBins) != 1 || loaded.RequiredBins[0] != "go" {
		t.Fatalf("required bins = %+v", loaded.RequiredBins)
	}

	resource, err := m.Load(context.Background(), LoadRequest{Name: "lark-doc", Resource: "references/create.md"})
	if err != nil {
		t.Fatal(err)
	}
	if resource.ResourceContent != "CREATE REFERENCE" {
		t.Fatalf("resource = %q", resource.ResourceContent)
	}
	if _, err := m.Load(context.Background(), LoadRequest{Name: "lark-doc", Resource: "../lark-shared/SKILL.md"}); err == nil {
		t.Fatal("expected traversal outside the skill directory to be rejected")
	}
}

func TestFSManagerLoadRejectsMissingBinaryDependency(t *testing.T) {
	m, managed, _ := testManager(t)
	writeSkillPackage(t, managed, "needs-cli", "---\nname: needs-cli\ndescription: external cli\nmetadata:\n  requires:\n    bins: [pchat-definitely-missing-cli]\n---\nINSTRUCTIONS\n", nil)

	_, err := m.Load(context.Background(), LoadRequest{Name: "needs-cli"})
	if err == nil || !strings.Contains(err.Error(), "pchat-definitely-missing-cli") {
		t.Fatalf("expected missing binary error, got %v", err)
	}
}

func TestFSManagerReadResourceRejectsSymlinkEscape(t *testing.T) {
	m, managed, _ := testManager(t)
	dir := writeSkillPackage(t, managed, "safe-skill", "---\nname: safe-skill\ndescription: safe\n---\nINSTRUCTIONS\n", nil)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "references")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	_, err := m.readResource(SkillInfo{Name: "safe-skill", Directory: dir}, "references/secret.txt")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "link") {
		t.Fatalf("expected linked resource path to be rejected, got %v", err)
	}
}

func TestFSManagerDoctorReportsMissingSkillDependency(t *testing.T) {
	m, managed, _ := testManager(t)
	writeSkillPackage(t, managed, "lark-doc", "---\nname: lark-doc\ndescription: manage docs\nmetadata:\n  requires:\n    skills: [lark-shared]\n---\nDOC\n", nil)

	catalog, err := m.Catalog(context.Background(), CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, diag := range catalog.Diagnostics {
		if diag.Code == "missing_skill_dependency" && diag.Skill == "lark-doc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want missing lark-shared", catalog.Diagnostics)
	}
}

func TestFSManagerImportPreservesCompletePackageAndPublishesAtomically(t *testing.T) {
	m, managed, _ := testManager(t)
	sourceRoot := t.TempDir()
	source := writeSkillPackage(t, sourceRoot, "source-name", "---\nname: imported\ndescription: full package\n---\nINSTRUCTIONS\n", map[string]string{
		"references/guide.md": "GUIDE",
		"scripts/check.ps1":   "Write-Output ok",
		"assets/template.txt": "TEMPLATE",
	})

	result, err := m.Apply(context.Background(), ChangeRequest{
		Action:     ChangeImport,
		SourcePath: source,
		Scope:      ScopeGlobalManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "imported" || result.Directory != filepath.Join(managed, "imported") {
		t.Fatalf("result = %+v", result)
	}
	if !result.Ready {
		t.Fatalf("imported package should be ready: %+v", result)
	}
	for _, name := range []string{"SKILL.md", "references/guide.md", "scripts/check.ps1", "assets/template.txt"} {
		if _, err := os.Stat(filepath.Join(result.Directory, filepath.FromSlash(name))); err != nil {
			t.Fatalf("package file %s missing after import: %v", name, err)
		}
	}

	catalog, err := m.Catalog(context.Background(), CatalogQuery{})
	if err != nil || len(catalog.Skills) != 1 || catalog.Skills[0].Name != "imported" {
		t.Fatalf("catalog after import = %+v, err=%v", catalog, err)
	}
}

func TestFSManagerImportDoesNotVerifyShadowedTargetAsReady(t *testing.T) {
	m, managed, _ := testManager(t)
	project := t.TempDir()
	writeSkillPackage(t, filepath.Join(project, ".p-chat", "skills"), "shared", "---\nname: shared\ndescription: project winner\n---\nPROJECT\n", nil)
	sourceRoot := t.TempDir()
	source := writeSkillPackage(t, sourceRoot, "shared", "---\nname: shared\ndescription: global candidate\n---\nGLOBAL\n", nil)

	result, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeImport, SourcePath: source, Scope: ScopeGlobalManaged, ProjectRoot: project,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready {
		t.Fatalf("shadowed global package must not be reported ready: %+v", result)
	}
	if result.Directory != filepath.Join(managed, "shared") {
		t.Fatalf("directory = %q", result.Directory)
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "post_install_shadowed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want post_install_shadowed", result.Diagnostics)
	}
}

func TestParseGitHubSourceSupportsRepositoryAndSkillDirectory(t *testing.T) {
	repo, err := parseGitHubSource("larksuite/cli", "lark-doc")
	if err != nil {
		t.Fatal(err)
	}
	if repo.repository != "https://github.com/larksuite/cli.git" || repo.subpath != filepath.Join("skills", "lark-doc") {
		t.Fatalf("repository source = %+v", repo)
	}

	dir, err := parseGitHubSource("https://github.com/larksuite/cli/tree/main/skills/lark-doc", "")
	if err != nil {
		t.Fatal(err)
	}
	if dir.branch != "main" || dir.subpath != filepath.Join("skills", "lark-doc") {
		t.Fatalf("directory source = %+v", dir)
	}

	if _, err := parseGitHubSource("ssh://example.com/repo", "x"); err == nil {
		t.Fatal("non-HTTPS/non-GitHub source should be rejected")
	}
	if _, err := parseGitHubSource("https://github.com/larksuite/cli/tree/main/skills/../../outside", ""); err == nil {
		t.Fatal("traversing GitHub package path should be rejected")
	}
}
