package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestFSManagerInstallsSkillAndDependenciesFromLarkCLI(t *testing.T) {
	m, managed, _ := testManager(t)
	m.runCommand = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "lark-cli" {
			t.Fatalf("binary = %q", binary)
		}
		switch strings.Join(args, " ") {
		case "skills list lark-doc":
			return []byte(`{"ok":true,"path":"lark-doc","entries":[{"path":"lark-doc/SKILL.md","is_dir":false},{"path":"lark-doc/references","is_dir":true}]}`), nil
		case "skills list lark-doc/references":
			return []byte(`{"ok":true,"path":"lark-doc/references","entries":[{"path":"lark-doc/references/guide.md","is_dir":false}]}`), nil
		case "skills read lark-doc SKILL.md --json":
			return []byte(`{"skill":"lark-doc","path":"SKILL.md","content":"---\nname: lark-doc\ndescription: docs\nmetadata:\n  requires:\n    skills: [lark-shared]\n---\nDOC\n"}`), nil
		case "skills read lark-doc references/guide.md --json":
			return []byte(`{"skill":"lark-doc","path":"references/guide.md","content":"GUIDE"}`), nil
		case "skills list lark-shared":
			return []byte(`{"ok":true,"path":"lark-shared","entries":[{"path":"lark-shared/SKILL.md","is_dir":false}]}`), nil
		case "skills read lark-shared SKILL.md --json":
			return []byte(`{"skill":"lark-shared","path":"SKILL.md","content":"---\nname: lark-shared\ndescription: auth\n---\nSHARED\n"}`), nil
		default:
			t.Fatalf("unexpected lark-cli call: %s", strings.Join(args, " "))
			return nil, nil
		}
	}

	result, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc", Scope: ScopeGlobalManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || strings.Join(result.Installed, ",") != "lark-doc,lark-shared" {
		t.Fatalf("result = %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(managed, "lark-doc", "references", "guide.md")); err != nil || string(data) != "GUIDE" {
		t.Fatalf("installed reference = %q, err=%v", data, err)
	}
	loaded, err := m.Load(context.Background(), LoadRequest{Name: "lark-doc"})
	if err != nil || !strings.Contains(loaded.Context, "SHARED") || !strings.Contains(loaded.Context, "DOC") {
		t.Fatalf("loaded = %+v, err=%v", loaded, err)
	}
}

func TestFSManagerRejectsTraversingCLISkillEntry(t *testing.T) {
	m, _, _ := testManager(t)
	m.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "skills list lark-doc" {
			return []byte(`{"ok":true,"path":"lark-doc","entries":[{"path":"lark-doc/../outside.md","is_dir":false}]}`), nil
		}
		t.Fatalf("unexpected CLI call: %s", strings.Join(args, " "))
		return nil, nil
	}

	_, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc", Scope: ScopeGlobalManaged,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid CLI Skill path") {
		t.Fatalf("err = %v, want traversal rejection", err)
	}
}

func TestFSManagerInstallsAllSkillsExposedByLarkCLI(t *testing.T) {
	m, managed, _ := testManager(t)
	m.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "skills list":
			return []byte(`{"ok":true,"skills":[{"name":"lark-doc"},{"name":"lark-shared"}],"count":2}`), nil
		case "skills list lark-doc":
			return []byte(`{"ok":true,"path":"lark-doc","entries":[{"path":"lark-doc/SKILL.md","is_dir":false}]}`), nil
		case "skills read lark-doc SKILL.md --json":
			return []byte(`{"skill":"lark-doc","path":"SKILL.md","content":"---\nname: lark-doc\ndescription: docs\n---\nDOC\n"}`), nil
		case "skills list lark-shared":
			return []byte(`{"ok":true,"path":"lark-shared","entries":[{"path":"lark-shared/SKILL.md","is_dir":false}]}`), nil
		case "skills read lark-shared SKILL.md --json":
			return []byte(`{"skill":"lark-shared","path":"SKILL.md","content":"---\nname: lark-shared\ndescription: auth\n---\nSHARED\n"}`), nil
		default:
			t.Fatalf("unexpected CLI call: %s", strings.Join(args, " "))
			return nil, nil
		}
	}

	result, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Scope: ScopeGlobalManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Name != "lark-cli" || result.Directory != managed || strings.Join(result.Installed, ",") != "lark-doc,lark-shared" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFSManagerRejectsMismatchedCLISkillIdentity(t *testing.T) {
	m, managed, _ := testManager(t)
	stubLarkCLISkillManifests(t, m, map[string]string{
		"lark-doc": "---\nname: lark-calendar\ndescription: wrong identity\n---\nDOC\n",
	})

	_, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc", Scope: ScopeGlobalManaged,
	})
	if err == nil || !strings.Contains(err.Error(), "declared name") {
		t.Fatalf("err = %v, want CLI Skill identity mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(managed, "lark-calendar")); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched package must not be published: %v", statErr)
	}
}

func TestFSManagerCLINamedInstallRequiresEveryDependencyReady(t *testing.T) {
	m, managed, _ := testManager(t)
	project := t.TempDir()
	writeSkillPackage(t, filepath.Join(project, ".p-chat", "skills"), "lark-shared", "---\nname: lark-shared\ndescription: project winner\n---\nPROJECT\n", nil)
	stubLarkCLISkillManifests(t, m, map[string]string{
		"lark-doc":    "---\nname: lark-doc\ndescription: docs\nmetadata:\n  requires:\n    skills: [lark-shared]\n---\nDOC\n",
		"lark-shared": "---\nname: lark-shared\ndescription: global candidate\n---\nGLOBAL\n",
	})

	result, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc",
		Scope: ScopeGlobalManaged, ProjectRoot: project,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready {
		t.Fatalf("shadowed dependency must make the whole CLI install unready: %+v", result)
	}
	if _, statErr := os.Stat(filepath.Join(managed, "lark-doc")); !os.IsNotExist(statErr) {
		t.Fatalf("unready CLI transaction must roll back its primary package: %v", statErr)
	}
}

func TestFSManagerRollsBackCLIBatchWhenVerificationFails(t *testing.T) {
	m, managed, _ := testManager(t)
	writeSkillPackage(t, managed, "lark-shared", "---\nname: lark-shared\ndescription: old\n---\nOLD\n", nil)
	stubLarkCLISkillManifests(t, m, map[string]string{
		"lark-doc":    "---\nname: lark-doc\ndescription: docs\nmetadata:\n  requires:\n    skills: [lark-shared]\n    bins: [pchat-definitely-missing-cli]\n---\nDOC\n",
		"lark-shared": "---\nname: lark-shared\ndescription: new\n---\nNEW\n",
	})

	result, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc", Scope: ScopeGlobalManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready {
		t.Fatalf("missing binary must make the CLI transaction unready: %+v", result)
	}
	data, readErr := os.ReadFile(filepath.Join(managed, "lark-shared", "SKILL.md"))
	if readErr != nil || !strings.Contains(string(data), "OLD") {
		t.Fatalf("previous dependency was not restored: content=%q err=%v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(managed, "lark-doc")); !os.IsNotExist(statErr) {
		t.Fatalf("failed primary package must be removed during rollback: %v", statErr)
	}
}

func TestFSManagerSharesSafetyBudgetAcrossCLIDependencies(t *testing.T) {
	base := t.TempDir()
	doc := "---\nname: lark-doc\ndescription: docs\nmetadata:\n  requires:\n    skills: [lark-shared]\n---\nDOC\n"
	shared := "---\nname: lark-shared\ndescription: shared\n---\nSHARED\n"
	maxBytes := len(doc)
	if len(shared) > maxBytes {
		maxBytes = len(shared)
	}
	m := NewFSManager(ManagerOptions{
		GlobalManagedDir: filepath.Join(base, "managed"),
		UserStandardDir:  filepath.Join(base, "standard"),
		MaxPackageBytes:  int64(maxBytes + 1),
	})
	stubLarkCLISkillManifests(t, m, map[string]string{"lark-doc": doc, "lark-shared": shared})

	_, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc", Scope: ScopeGlobalManaged,
	})
	if err == nil || !strings.Contains(err.Error(), "safety limits") {
		t.Fatalf("err = %v, want aggregate CLI byte limit", err)
	}
}

func TestBoundedCommandOutputCapsRetainedBytes(t *testing.T) {
	output := &boundedCommandOutput{limit: 4}
	written, err := output.Write([]byte("123456"))
	if err != nil || written != 6 {
		t.Fatalf("Write = %d, %v", written, err)
	}
	if got := string(output.bytes()); got != "1234" || !output.exceeded {
		t.Fatalf("retained = %q exceeded=%v", got, output.exceeded)
	}
}

func TestFSManagerCatalogWaitsForFilesystemMutation(t *testing.T) {
	m, _, _ := testManager(t)
	skillFilesystemMu.Lock()
	locked := true
	defer func() {
		if locked {
			skillFilesystemMu.Unlock()
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := m.Catalog(context.Background(), CatalogQuery{})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Catalog returned during an active Skill mutation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	skillFilesystemMu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Catalog did not resume after the Skill mutation completed")
	}
}

func stubLarkCLISkillManifests(t *testing.T, m *FSManager, manifests map[string]string) {
	t.Helper()
	m.runCommand = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "lark-cli" {
			t.Fatalf("binary = %q", binary)
		}
		if len(args) == 3 && args[0] == "skills" && args[1] == "list" {
			name := args[2]
			if _, ok := manifests[name]; !ok {
				t.Fatalf("unexpected Skill listing: %s", name)
			}
			return []byte(`{"ok":true,"path":"` + name + `","entries":[{"path":"` + name + `/SKILL.md","is_dir":false}]}`), nil
		}
		if len(args) == 5 && args[0] == "skills" && args[1] == "read" && args[3] == "SKILL.md" && args[4] == "--json" {
			name := args[2]
			manifest, ok := manifests[name]
			if !ok {
				t.Fatalf("unexpected Skill read: %s", name)
			}
			encoded, err := json.Marshal(cliSkillRead{Skill: name, Path: "SKILL.md", Content: manifest})
			if err != nil {
				t.Fatal(err)
			}
			return encoded, nil
		}
		t.Fatalf("unexpected lark-cli call: %s", strings.Join(args, " "))
		return nil, nil
	}
}

func TestFSManagerImportsRealLarkCLISkillWhenEnabled(t *testing.T) {
	if os.Getenv("PCHAT_TEST_LARK_CLI") != "1" {
		t.Skip("set PCHAT_TEST_LARK_CLI=1 for the host integration test")
	}
	m, managed, _ := testManager(t)
	result, err := m.Apply(context.Background(), ChangeRequest{
		Action: ChangeInstall, SourceCLI: "lark-cli", Name: "lark-doc", Scope: ScopeGlobalManaged,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || len(result.Installed) < 2 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(managed, "lark-doc", "references", "lark-doc-fetch.md")); err != nil {
		t.Fatalf("real lark-doc reference missing: %v", err)
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
