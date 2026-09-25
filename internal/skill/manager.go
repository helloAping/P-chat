package skill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/paths"
	"gopkg.in/yaml.v3"
)

const (
	defaultMaxPackageFiles   int64 = 512
	defaultMaxPackageBytes   int64 = 32 << 20
	defaultMaxFileBytes      int64 = 8 << 20
	defaultMaxImportPackages int64 = 128
)

var skillFilesystemMu sync.RWMutex

// Scope 标识生效 Skill 包的来源位置。
// Scope identifies where an effective Skill package came from.
type Scope string

const (
	ScopeProjectManaged  Scope = "project_managed"
	ScopeProjectStandard Scope = "project_standard"
	ScopeGlobalManaged   Scope = "global_managed"
	ScopeUserStandard    Scope = "user_standard"
)

// ChangeAction 表示 Manager 的状态变更操作。
// ChangeAction is a state-changing Manager operation.
type ChangeAction string

const (
	ChangeImport  ChangeAction = "import"
	ChangeInstall ChangeAction = "install"
	ChangeRemove  ChangeAction = "remove"
)

// Manager 是 Agent、HTTP、CLI 与工具共用的唯一 Skill 边界。
// Manager is the single boundary used by Agent, HTTP, CLI, and tools.
type Manager interface {
	Catalog(context.Context, CatalogQuery) (Catalog, error)
	Load(context.Context, LoadRequest) (LoadedSkill, error)
	Apply(context.Context, ChangeRequest) (ChangeResult, error)
}

// ManagerOptions 配置基于文件系统的 Manager。
// ManagerOptions configures the filesystem-backed Manager.
type ManagerOptions struct {
	GlobalManagedDir string
	UserStandardDir  string
	MaxPackageFiles  int64
	MaxPackageBytes  int64
	MaxFileBytes     int64
}

// FSManager 使用目录包实现 Manager。
// FSManager implements Manager using directory packages.
type FSManager struct {
	globalManagedDir string
	userStandardDir  string
	maxPackageFiles  int64
	maxPackageBytes  int64
	maxFileBytes     int64
}

// CatalogQuery 选择需要应用 Skill 覆盖规则的项目。
// CatalogQuery selects the project whose Skill overlays should be applied.
type CatalogQuery struct {
	ProjectRoot string
}

// SkillSource 标识一个物理 Skill 包位置。
// SkillSource identifies one physical package location.
type SkillSource struct {
	Scope     Scope  `json:"scope"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
}

// SkillInfo 是发现阶段元数据，刻意不包含指令正文。
// SkillInfo is discovery metadata; it deliberately excludes instruction text.
type SkillInfo struct {
	Name           string        `json:"name"`
	Description    string        `json:"description,omitempty"`
	Scope          Scope         `json:"scope"`
	Directory      string        `json:"directory"`
	Path           string        `json:"path"`
	RequiredSkills []string      `json:"required_skills,omitempty"`
	RequiredBins   []string      `json:"required_bins,omitempty"`
	Resources      []string      `json:"resources,omitempty"`
	Shadowed       []SkillSource `json:"shadowed,omitempty"`
}

// Diagnostic 描述非致命的发现或依赖问题。
// Diagnostic describes a non-fatal discovery or dependency problem.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Skill    string `json:"skill,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// Catalog 是生效 Skills 与诊断信息的确定性快照。
// Catalog is a deterministic snapshot of effective Skills and diagnostics.
type Catalog struct {
	Skills      []SkillInfo  `json:"skills"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// LoadRequest 用于加载 Skill 指令包或其中一个资源。
// LoadRequest loads a Skill instruction package or one contained resource.
type LoadRequest struct {
	ProjectRoot string
	Name        string
	Resource    string
}

// LoadedEntry 是一个完整加载的 Skill，依赖项会排在主 Skill 之前。
// LoadedEntry is one fully loaded Skill, with dependencies ordered first.
type LoadedEntry struct {
	SkillInfo
	Content string `json:"content"`
}

// LoadedSkill 是可见 start 事件后传给 Agent 的加载结果。
// LoadedSkill is the result passed to the Agent after a visible start event.
type LoadedSkill struct {
	Name            string        `json:"name"`
	Skills          []LoadedEntry `json:"skills,omitempty"`
	Context         string        `json:"context,omitempty"`
	RequiredBins    []string      `json:"required_bins,omitempty"`
	Resource        string        `json:"resource,omitempty"`
	ResourceContent string        `json:"resource_content,omitempty"`
}

// ChangeRequest 描述托管 Skill 包的安装、导入或移除操作。
// ChangeRequest describes an install, import, or removal of managed Skill packages.
type ChangeRequest struct {
	Action      ChangeAction
	SourcePath  string
	SourceURL   string
	Name        string
	Scope       Scope
	ProjectRoot string
}

// ChangeResult 报告发布目录及变更后的健康状态。
// ChangeResult reports the published package location and post-change health.
type ChangeResult struct {
	Name        string       `json:"name"`
	Scope       Scope        `json:"scope"`
	Directory   string       `json:"directory"`
	Ready       bool         `json:"ready"`
	Installed   []string     `json:"installed,omitempty"`
	RolledBack  bool         `json:"rolled_back,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type skillManifest struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Requires    requirements `yaml:"requires"`
	Metadata    struct {
		Requires requirements `yaml:"requires"`
	} `yaml:"metadata"`
}

type requirements struct {
	Skills []string `yaml:"skills"`
	Bins   []string `yaml:"bins"`
}

type discoveredSkill struct {
	info    SkillInfo
	content string
}

// NewFSManager 创建带安全包限制的文件系统 Manager。
// NewFSManager creates a filesystem Manager with safe package limits.
func NewFSManager(opts ManagerOptions) *FSManager {
	if strings.TrimSpace(opts.GlobalManagedDir) == "" {
		opts.GlobalManagedDir = paths.GlobalSkillsDir()
	}
	if strings.TrimSpace(opts.UserStandardDir) == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			opts.UserStandardDir = filepath.Join(home, ".agents", "skills")
		}
	}
	if opts.MaxPackageFiles <= 0 {
		opts.MaxPackageFiles = defaultMaxPackageFiles
	}
	if opts.MaxPackageBytes <= 0 {
		opts.MaxPackageBytes = defaultMaxPackageBytes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = defaultMaxFileBytes
	}
	return &FSManager{
		globalManagedDir: filepath.Clean(opts.GlobalManagedDir),
		userStandardDir:  filepath.Clean(opts.UserStandardDir),
		maxPackageFiles:  opts.MaxPackageFiles,
		maxPackageBytes:  opts.MaxPackageBytes,
		maxFileBytes:     opts.MaxFileBytes,
	}
}

// NewManager 返回进程默认 Manager。
// NewManager returns the default process Manager.
func NewManager() *FSManager {
	return NewFSManager(ManagerOptions{})
}

// Catalog 按优先级发现 Skills 并校验依赖。
// Catalog discovers Skills in precedence order and validates dependencies.
func (m *FSManager) Catalog(ctx context.Context, query CatalogQuery) (Catalog, error) {
	skillFilesystemMu.RLock()
	defer skillFilesystemMu.RUnlock()
	return m.catalog(ctx, query)
}

func (m *FSManager) catalog(ctx context.Context, query CatalogQuery) (Catalog, error) {
	type root struct {
		dir   string
		scope Scope
	}
	roots := make([]root, 0, 4)
	if query.ProjectRoot != "" {
		roots = append(roots,
			root{filepath.Join(query.ProjectRoot, ".p-chat", "skills"), ScopeProjectManaged},
			root{filepath.Join(query.ProjectRoot, ".agents", "skills"), ScopeProjectStandard},
		)
	}
	roots = append(roots,
		root{m.globalManagedDir, ScopeGlobalManaged},
		root{m.userStandardDir, ScopeUserStandard},
	)

	effective := make(map[string]SkillInfo)
	var diagnostics []Diagnostic
	for _, candidate := range roots {
		if err := ctx.Err(); err != nil {
			return Catalog{}, err
		}
		found, diags, err := m.scanRoot(candidate.dir, candidate.scope)
		if err != nil {
			return Catalog{}, err
		}
		diagnostics = append(diagnostics, diags...)
		for _, item := range found {
			if winner, exists := effective[item.Name]; exists {
				winner.Shadowed = append(winner.Shadowed, SkillSource{
					Scope: item.Scope, Directory: item.Directory, Path: item.Path,
				})
				effective[item.Name] = winner
				continue
			}
			effective[item.Name] = item
		}
	}

	skills := make([]SkillInfo, 0, len(effective))
	for _, item := range effective {
		skills = append(skills, item)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	for _, item := range skills {
		for _, dependency := range item.RequiredSkills {
			if _, ok := effective[dependency]; !ok {
				diagnostics = append(diagnostics, Diagnostic{
					Code: "missing_skill_dependency", Severity: "error", Skill: item.Name,
					Source:  item.Path,
					Message: fmt.Sprintf("Skill %q requires missing Skill %q", item.Name, dependency),
				})
			}
		}
		for _, bin := range item.RequiredBins {
			if _, err := exec.LookPath(bin); err != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Code: "missing_binary_dependency", Severity: "warning", Skill: item.Name,
					Source:  item.Path,
					Message: fmt.Sprintf("Skill %q requires binary %q, but it is not on PATH", item.Name, bin),
				})
			}
		}
	}
	sortDiagnostics(diagnostics)
	return Catalog{Skills: skills, Diagnostics: diagnostics}, nil
}

func (m *FSManager) scanRoot(root string, scope Scope) ([]SkillInfo, []Diagnostic, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read Skill root %s: %w", root, err)
	}
	var skills []SkillInfo
	var diagnostics []Diagnostic
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		found, diags, err := m.inspectPackage(dir, entry.Name(), scope, true)
		diagnostics = append(diagnostics, diags...)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "invalid_skill", Severity: "error", Source: dir, Message: err.Error(),
			})
			continue
		}
		skills = append(skills, found.info)
	}
	return skills, diagnostics, nil
}

func (m *FSManager) inspectPackage(dir, directoryName string, scope Scope, allowLegacy bool) (discoveredSkill, []Diagnostic, error) {
	manifestPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(manifestPath)
	if err != nil && allowLegacy {
		manifestPath = filepath.Join(dir, "README.md")
		data, err = os.ReadFile(manifestPath)
	}
	if err != nil {
		return discoveredSkill{}, nil, fmt.Errorf("Skill package %s has no readable SKILL.md", dir)
	}
	if int64(len(data)) > m.maxFileBytes {
		return discoveredSkill{}, nil, fmt.Errorf("Skill manifest exceeds %d bytes", m.maxFileBytes)
	}
	content := string(data)
	manifest, err := parseManifest(content)
	if err != nil {
		return discoveredSkill{}, nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = directoryName
	}
	if err := validateSkillName(name); err != nil {
		return discoveredSkill{}, nil, err
	}
	description := strings.TrimSpace(manifest.Description)
	if description == "" {
		description = extractDescription(content)
	}
	requiredSkills := append([]string(nil), manifest.Requires.Skills...)
	requiredSkills = append(requiredSkills, manifest.Metadata.Requires.Skills...)
	requiredBins := append([]string(nil), manifest.Requires.Bins...)
	requiredBins = append(requiredBins, manifest.Metadata.Requires.Bins...)
	requiredSkills = uniqueSorted(requiredSkills)
	requiredBins = uniqueSorted(requiredBins)
	var resources []string
	// Catalog 扫描只读取元数据，不递归所有包资源。
	// Catalog scans stay metadata-only and do not recurse through every package resource.
	// 完整包校验只在导入和按需 Load 时执行。
	// Full package validation happens on import and on-demand Load.
	if !allowLegacy {
		resources, err = m.listPackageFiles(dir)
		if err != nil {
			return discoveredSkill{}, nil, err
		}
	}
	var diagnostics []Diagnostic
	if name != directoryName {
		diagnostics = append(diagnostics, Diagnostic{
			Code: "directory_name_mismatch", Severity: "warning", Skill: name, Source: manifestPath,
			Message: fmt.Sprintf("Skill %q is stored in directory %q", name, directoryName),
		})
	}
	return discoveredSkill{info: SkillInfo{
		Name: name, Description: description, Scope: scope, Directory: dir, Path: manifestPath,
		RequiredSkills: requiredSkills, RequiredBins: requiredBins, Resources: resources,
	}, content: content}, diagnostics, nil
}

func parseManifest(content string) (skillManifest, error) {
	meta, _, ok := splitFrontmatter(content)
	if !ok {
		return skillManifest{}, nil
	}
	var manifest skillManifest
	if err := yaml.Unmarshal([]byte(meta), &manifest); err != nil {
		return skillManifest{}, err
	}
	return manifest, nil
}

func validateSkillName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid Skill name %q", name)
	}
	return nil
}

func (m *FSManager) listPackageFiles(root string) ([]string, error) {
	var files []string
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill package contains unsupported symlink: %s", path)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve Skill package path %s: %w", path, err)
		}
		if !samePath(path, resolved) {
			return fmt.Errorf("Skill package contains unsupported link: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Skill package contains unsupported non-regular file: %s", path)
		}
		if info.Size() > m.maxFileBytes {
			return fmt.Errorf("Skill package file %s exceeds %d bytes", path, m.maxFileBytes)
		}
		total += info.Size()
		if total > m.maxPackageBytes {
			return fmt.Errorf("Skill package exceeds %d bytes", m.maxPackageBytes)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		if int64(len(files)) > m.maxPackageFiles {
			return fmt.Errorf("Skill package exceeds %d files", m.maxPackageFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// Load 每次调用都重新解析选中包，使外部安装结果立即可见。
// Load resolves the selected package on every call, so external installers are visible immediately.
func (m *FSManager) Load(ctx context.Context, request LoadRequest) (LoadedSkill, error) {
	skillFilesystemMu.RLock()
	defer skillFilesystemMu.RUnlock()
	return m.load(ctx, request)
}

func (m *FSManager) load(ctx context.Context, request LoadRequest) (LoadedSkill, error) {
	if err := validateSkillName(request.Name); err != nil {
		return LoadedSkill{}, err
	}
	catalog, err := m.catalog(ctx, CatalogQuery{ProjectRoot: request.ProjectRoot})
	if err != nil {
		return LoadedSkill{}, err
	}
	byName := make(map[string]SkillInfo, len(catalog.Skills))
	for _, item := range catalog.Skills {
		byName[item.Name] = item
	}
	primary, ok := byName[request.Name]
	if !ok {
		return LoadedSkill{}, fmt.Errorf("Skill %q is not installed", request.Name)
	}
	if request.Resource != "" {
		content, err := m.readResource(primary, request.Resource)
		if err != nil {
			return LoadedSkill{}, err
		}
		return LoadedSkill{Name: request.Name, Resource: filepath.ToSlash(request.Resource), ResourceContent: content}, nil
	}

	state := make(map[string]uint8)
	var entries []LoadedEntry
	var requiredBins []string
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("Skill dependency cycle includes %q", name)
		}
		if state[name] == 2 {
			return nil
		}
		item, exists := byName[name]
		if !exists {
			return fmt.Errorf("Skill %q requires missing Skill %q", request.Name, name)
		}
		state[name] = 1
		for _, dependency := range item.RequiredSkills {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		data, err := os.ReadFile(item.Path)
		if err != nil {
			return fmt.Errorf("read Skill %q: %w", name, err)
		}
		resources, err := m.listPackageFiles(item.Directory)
		if err != nil {
			return fmt.Errorf("validate Skill %q package: %w", name, err)
		}
		item.Resources = resources
		entries = append(entries, LoadedEntry{SkillInfo: item, Content: string(data)})
		requiredBins = append(requiredBins, item.RequiredBins...)
		state[name] = 2
		return nil
	}
	if err := visit(request.Name); err != nil {
		return LoadedSkill{}, err
	}
	for _, bin := range uniqueSorted(requiredBins) {
		if _, err := exec.LookPath(bin); err != nil {
			return LoadedSkill{}, fmt.Errorf("Skill %q requires missing executable %q", request.Name, bin)
		}
	}
	var prompt strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&prompt, "## Skill: %s\n\n%s\n\n", entry.Name, entry.Content)
	}
	return LoadedSkill{
		Name: request.Name, Skills: entries, Context: strings.TrimSpace(prompt.String()),
		RequiredBins: uniqueSorted(requiredBins),
	}, nil
}

func (m *FSManager) readResource(info SkillInfo, resource string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(resource)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resource path %q escapes Skill %q", resource, info.Name)
	}
	base, err := filepath.Abs(info.Directory)
	if err != nil {
		return "", fmt.Errorf("resolve Skill %q directory: %w", info.Name, err)
	}
	path := filepath.Join(base, clean)
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resource path %q escapes Skill %q", resource, info.Name)
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve Skill %q directory links: %w", info.Name, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Skill resource %q links: %w", resource, err)
	}
	if !samePath(path, resolvedPath) || !isPathWithin(resolvedBase, resolvedPath) {
		return "", fmt.Errorf("Skill resource %q uses a link outside the package boundary", resource)
	}
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read Skill resource %q: %w", resource, err)
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("Skill resource %q is not a regular file", resource)
	}
	if fileInfo.Size() > m.maxFileBytes {
		return "", fmt.Errorf("Skill resource %q exceeds %d bytes", resource, m.maxFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Skill resource %q: %w", resource, err)
	}
	return string(data), nil
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func isPathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Apply 导入或移除托管 Skill 包，并返回最新诊断快照。
// Apply imports or removes a managed Skill package, then returns a fresh diagnostic snapshot.
func (m *FSManager) Apply(ctx context.Context, request ChangeRequest) (ChangeResult, error) {
	targetRoot, err := m.managedRoot(request.Scope, request.ProjectRoot)
	if err != nil {
		return ChangeResult{}, err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return ChangeResult{}, fmt.Errorf("create Skill root: %w", err)
	}
	switch request.Action {
	case ChangeImport:
		return m.importSource(ctx, request, targetRoot)
	case ChangeInstall:
		if strings.TrimSpace(request.SourceURL) == "" {
			return ChangeResult{}, errors.New("source_url is required for Skill install")
		}
		return m.installRemotePackage(ctx, request, targetRoot)
	case ChangeRemove:
		if err := validateSkillName(request.Name); err != nil {
			return ChangeResult{}, err
		}
		skillFilesystemMu.Lock()
		defer skillFilesystemMu.Unlock()
		target := filepath.Join(targetRoot, request.Name)
		if err := ensureDirectChild(targetRoot, target); err != nil {
			return ChangeResult{}, err
		}
		if err := os.RemoveAll(target); err != nil {
			return ChangeResult{}, fmt.Errorf("remove Skill %q: %w", request.Name, err)
		}
		catalog, err := m.catalog(ctx, CatalogQuery{ProjectRoot: request.ProjectRoot})
		if err != nil {
			return ChangeResult{}, err
		}
		return ChangeResult{Name: request.Name, Scope: request.Scope, Directory: target, Diagnostics: catalog.Diagnostics}, nil
	default:
		return ChangeResult{}, fmt.Errorf("unsupported Skill change action %q", request.Action)
	}
}

type stagedSkillPackage struct {
	name        string
	stage       string
	target      string
	backup      string
	hadPrevious bool
	published   bool
}

// importSource 接受单个 Skill 包或由任意外部工具导出的 Skill 集合目录。
// importSource accepts one Skill package or a Skill collection directory exported by any external tool.
func (m *FSManager) importSource(ctx context.Context, request ChangeRequest, targetRoot string) (ChangeResult, error) {
	rawSource := strings.TrimSpace(request.SourcePath)
	if rawSource == "" {
		return ChangeResult{}, errors.New("Skill source_path is required")
	}
	source, err := filepath.Abs(rawSource)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("resolve Skill source: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("inspect Skill source: %w", err)
	}
	if !info.IsDir() {
		return ChangeResult{}, errors.New("Skill import source must be a directory")
	}
	if manifest, manifestErr := os.Stat(filepath.Join(source, "SKILL.md")); manifestErr == nil && manifest.Mode().IsRegular() {
		discovered, _, inspectErr := m.inspectPackage(source, filepath.Base(source), request.Scope, false)
		if inspectErr != nil {
			return ChangeResult{}, inspectErr
		}
		name := discovered.info.Name
		if requestedName := strings.TrimSpace(request.Name); requestedName != "" && requestedName != name {
			return ChangeResult{}, fmt.Errorf("requested Skill %q, package declared %q", requestedName, name)
		}
		diagnostics, ready, publishErr := m.publishSkillBatch(
			ctx, request, targetRoot, map[string]string{name: source}, []string{name},
		)
		if publishErr != nil {
			return ChangeResult{}, publishErr
		}
		result := ChangeResult{
			Name: name, Scope: request.Scope, Directory: filepath.Join(targetRoot, name),
			Ready: ready, RolledBack: !ready, Diagnostics: diagnostics,
		}
		if ready {
			result.Installed = []string{name}
		}
		return result, nil
	} else if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return ChangeResult{}, fmt.Errorf("inspect Skill manifest: %w", manifestErr)
	}
	return m.importCollection(ctx, request, source, targetRoot)
}

// importCollection 发现集合中的包，并在指定名称时自动包含集合内声明的依赖。
// importCollection discovers packages and includes declared in-collection dependencies for a named import.
func (m *FSManager) importCollection(
	ctx context.Context,
	request ChangeRequest,
	sourceRoot, targetRoot string,
) (ChangeResult, error) {
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("read Skill collection: %w", err)
	}
	sources := make(map[string]string)
	metadata := make(map[string]SkillInfo)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return ChangeResult{}, fmt.Errorf("Skill collection contains unsupported symlink: %s", filepath.Join(sourceRoot, entry.Name()))
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(sourceRoot, entry.Name())
		manifest, statErr := os.Stat(filepath.Join(dir, "SKILL.md"))
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return ChangeResult{}, fmt.Errorf("inspect Skill collection entry %q: %w", entry.Name(), statErr)
		}
		if !manifest.Mode().IsRegular() {
			return ChangeResult{}, fmt.Errorf("Skill collection entry %q has a non-regular SKILL.md", entry.Name())
		}
		discovered, _, inspectErr := m.inspectPackage(dir, entry.Name(), request.Scope, true)
		if inspectErr != nil {
			return ChangeResult{}, fmt.Errorf("validate Skill collection entry %q: %w", entry.Name(), inspectErr)
		}
		name := discovered.info.Name
		if previous, exists := sources[name]; exists {
			return ChangeResult{}, fmt.Errorf("Skill collection declares duplicate name %q in %s and %s", name, previous, dir)
		}
		sources[name] = dir
		metadata[name] = discovered.info
		if int64(len(sources)) > defaultMaxImportPackages {
			return ChangeResult{}, fmt.Errorf("Skill collection exceeds %d packages", defaultMaxImportPackages)
		}
	}
	if len(sources) == 0 {
		return ChangeResult{}, errors.New("Skill collection does not contain any child package with SKILL.md")
	}

	requestedName := strings.TrimSpace(request.Name)
	if requestedName != "" {
		if err := validateSkillName(requestedName); err != nil {
			return ChangeResult{}, err
		}
		if _, ok := sources[requestedName]; !ok {
			return ChangeResult{}, fmt.Errorf("Skill collection does not contain %q", requestedName)
		}
	}
	state := make(map[string]uint8)
	order := make([]string, 0, len(sources))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("Skill dependency cycle through %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, dependency := range metadata[name].RequiredSkills {
			if _, bundled := sources[dependency]; bundled {
				if err := visit(dependency); err != nil {
					return fmt.Errorf("resolve dependency of %q: %w", name, err)
				}
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	if requestedName != "" {
		if err := visit(requestedName); err != nil {
			return ChangeResult{}, err
		}
	} else {
		for _, name := range sortedMapKeys(sources) {
			if err := visit(name); err != nil {
				return ChangeResult{}, err
			}
		}
	}
	for _, name := range order {
		_, _, inspectErr := m.inspectPackage(sources[name], filepath.Base(sources[name]), request.Scope, false)
		if inspectErr != nil {
			return ChangeResult{}, fmt.Errorf("validate selected Skill %q: %w", name, inspectErr)
		}
	}

	result := ChangeResult{Name: filepath.Base(sourceRoot), Scope: request.Scope, Directory: targetRoot}
	if requestedName != "" {
		result.Name = requestedName
		result.Directory = filepath.Join(targetRoot, requestedName)
	}
	diagnostics, ready, err := m.publishSkillBatch(ctx, request, targetRoot, sources, order)
	if err != nil {
		return ChangeResult{}, err
	}
	result.Ready = ready
	result.RolledBack = !ready
	result.Diagnostics = diagnostics
	if ready {
		result.Installed = uniqueSorted(order)
	}
	return result, nil
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// publishSkillBatch 先完整 staging，再统一发布并验证；任何失败都会恢复旧包。
// publishSkillBatch stages the complete set before publishing and restores old packages on any failure.
func (m *FSManager) publishSkillBatch(
	ctx context.Context,
	request ChangeRequest,
	targetRoot string,
	sources map[string]string,
	order []string,
) ([]Diagnostic, bool, error) {
	transactionRoot, err := os.MkdirTemp(filepath.Dir(targetRoot), ".pchat-skill-transaction-")
	if err != nil {
		return nil, false, fmt.Errorf("create Skill transaction directory: %w", err)
	}
	preserveTransaction := false
	defer func() {
		if !preserveTransaction {
			_ = os.RemoveAll(transactionRoot)
		}
	}()
	failWithRollback := func(changed []stagedSkillPackage, cause error) error {
		if rollbackErr := rollbackSkillBatch(changed); rollbackErr != nil {
			preserveTransaction = true
			return fmt.Errorf("%w; rollback failed: %v; recovery data remains at %s", cause, rollbackErr, transactionRoot)
		}
		return cause
	}

	packages := make([]stagedSkillPackage, 0, len(order))
	budget := &skillImportBudget{maxFiles: m.maxPackageFiles, maxBytes: m.maxPackageBytes}
	for _, name := range order {
		target := filepath.Join(targetRoot, name)
		if err := ensureDirectChild(targetRoot, target); err != nil {
			return nil, false, err
		}
		stage := filepath.Join(transactionRoot, name+".stage")
		if err := os.MkdirAll(stage, 0o755); err != nil {
			return nil, false, fmt.Errorf("create staging directory for Skill %q: %w", name, err)
		}
		item := stagedSkillPackage{
			name: name, stage: stage, target: target,
			backup: filepath.Join(transactionRoot, name+".backup"),
		}
		packages = append(packages, item)
		if err := m.copyPackage(ctx, sources[name], stage, budget); err != nil {
			return nil, false, fmt.Errorf("stage Skill %q: %w", name, err)
		}
		discovered, _, err := m.inspectPackage(stage, name, request.Scope, false)
		if err != nil {
			return nil, false, fmt.Errorf("validate staged Skill %q: %w", name, err)
		}
		if discovered.info.Name != name {
			return nil, false, fmt.Errorf("staged Skill %q declared name %q", name, discovered.info.Name)
		}
	}

	skillFilesystemMu.Lock()
	defer skillFilesystemMu.Unlock()
	for i := range packages {
		if _, err := os.Lstat(packages[i].target); err == nil {
			if err := os.Rename(packages[i].target, packages[i].backup); err != nil {
				cause := fmt.Errorf("back up Skill %q: %w", packages[i].name, err)
				return nil, false, failWithRollback(packages[:i], cause)
			}
			packages[i].hadPrevious = true
		} else if !os.IsNotExist(err) {
			cause := fmt.Errorf("inspect existing Skill %q: %w", packages[i].name, err)
			return nil, false, failWithRollback(packages[:i], cause)
		}
		if err := os.Rename(packages[i].stage, packages[i].target); err != nil {
			cause := fmt.Errorf("publish Skill %q: %w", packages[i].name, err)
			return nil, false, failWithRollback(packages[:i+1], cause)
		}
		packages[i].published = true
	}

	diagnostics, ready, err := m.verifySkillBatch(ctx, request, packages)
	if err != nil {
		return nil, false, failWithRollback(packages, err)
	}
	if !ready {
		if rollbackErr := rollbackSkillBatch(packages); rollbackErr != nil {
			preserveTransaction = true
			return nil, false, fmt.Errorf("rollback unready Skill transaction: %w; recovery data remains at %s", rollbackErr, transactionRoot)
		}
		return diagnostics, false, nil
	}
	return diagnostics, true, nil
}

// verifySkillBatch 确认事务内每个包都是当前生效包且可完整加载。
// verifySkillBatch confirms every package in the transaction is effective and fully loadable.
func (m *FSManager) verifySkillBatch(
	ctx context.Context,
	request ChangeRequest,
	packages []stagedSkillPackage,
) ([]Diagnostic, bool, error) {
	catalog, err := m.catalog(ctx, CatalogQuery{ProjectRoot: request.ProjectRoot})
	if err != nil {
		return nil, false, fmt.Errorf("scan Skills after collection import: %w", err)
	}
	diagnostics := append([]Diagnostic(nil), catalog.Diagnostics...)
	ready := true
	for _, item := range packages {
		var effective *SkillInfo
		for i := range catalog.Skills {
			if catalog.Skills[i].Name == item.name {
				effective = &catalog.Skills[i]
				break
			}
		}
		if effective == nil || effective.Scope != request.Scope || !samePath(effective.Directory, item.target) {
			message := "installed package is not the effective Skill in the current project context"
			if effective != nil {
				message = fmt.Sprintf("installed package is shadowed by %s at %s", effective.Scope, effective.Directory)
			}
			diagnostics = append(diagnostics, Diagnostic{
				Code: "post_install_shadowed", Severity: "error", Skill: item.name,
				Source: filepath.Join(item.target, "SKILL.md"), Message: message,
			})
			ready = false
			continue
		}
		if _, loadErr := m.load(ctx, LoadRequest{ProjectRoot: request.ProjectRoot, Name: item.name}); loadErr != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Code: "post_install_verification_failed", Severity: "error", Skill: item.name,
				Source: filepath.Join(item.target, "SKILL.md"), Message: loadErr.Error(),
			})
			ready = false
		}
	}
	sortDiagnostics(diagnostics)
	return diagnostics, ready, nil
}

func rollbackSkillBatch(packages []stagedSkillPackage) error {
	var rollbackErr error
	for i := len(packages) - 1; i >= 0; i-- {
		item := packages[i]
		if item.published {
			if err := os.RemoveAll(item.target); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove published Skill %q: %w", item.name, err))
				continue
			}
		}
		if item.hadPrevious {
			if err := os.Rename(item.backup, item.target); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore Skill %q from %s: %w", item.name, item.backup, err))
			}
		}
	}
	return rollbackErr
}

type githubSource struct {
	repository string
	branch     string
	subpath    string
}

func parseGitHubSource(raw, name string) (githubSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return githubSource{}, errors.New("source_url is required")
	}
	if !strings.Contains(raw, "://") && strings.Count(strings.Trim(raw, "/"), "/") == 1 {
		raw = "https://github.com/" + strings.Trim(raw, "/")
	}
	const githubPrefix = "https://github.com/"
	const rawPrefix = "https://raw.githubusercontent.com/"
	var source githubSource
	switch {
	case strings.HasPrefix(raw, githubPrefix):
		parts := strings.Split(strings.Trim(strings.TrimPrefix(raw, githubPrefix), "/"), "/")
		if len(parts) < 2 {
			return githubSource{}, errors.New("GitHub source must include owner and repository")
		}
		owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
		source.repository = fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
		if len(parts) >= 5 && (parts[2] == "tree" || parts[2] == "blob") {
			source.branch = parts[3]
			rest := parts[4:]
			if parts[2] == "blob" && len(rest) > 0 && strings.EqualFold(rest[len(rest)-1], "SKILL.md") {
				rest = rest[:len(rest)-1]
			}
			source.subpath = filepath.Join(rest...)
		}
	case strings.HasPrefix(raw, rawPrefix):
		parts := strings.Split(strings.Trim(strings.TrimPrefix(raw, rawPrefix), "/"), "/")
		if len(parts) < 4 {
			return githubSource{}, errors.New("raw GitHub source must include owner, repository, ref, and path")
		}
		source.repository = fmt.Sprintf("https://github.com/%s/%s.git", parts[0], strings.TrimSuffix(parts[1], ".git"))
		source.branch = parts[2]
		rest := parts[3:]
		if len(rest) > 0 && strings.EqualFold(rest[len(rest)-1], "SKILL.md") {
			rest = rest[:len(rest)-1]
		}
		source.subpath = filepath.Join(rest...)
	default:
		return githubSource{}, errors.New("only HTTPS GitHub Skill sources are supported")
	}
	if source.branch != "" && strings.HasPrefix(source.branch, "-") {
		return githubSource{}, errors.New("invalid GitHub branch")
	}
	if source.subpath == "" && name != "" {
		source.subpath = filepath.Join("skills", name)
	}
	if source.subpath != "" {
		clean := filepath.Clean(source.subpath)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return githubSource{}, errors.New("GitHub Skill path must stay inside the repository")
		}
		source.subpath = clean
	}
	return source, nil
}

func (m *FSManager) installRemotePackage(ctx context.Context, request ChangeRequest, targetRoot string) (ChangeResult, error) {
	source, err := parseGitHubSource(request.SourceURL, request.Name)
	if err != nil {
		return ChangeResult{}, err
	}
	cloneRoot, err := os.MkdirTemp("", "pchat-skill-clone-")
	if err != nil {
		return ChangeResult{}, fmt.Errorf("create Skill clone directory: %w", err)
	}
	defer os.RemoveAll(cloneRoot)
	cloneDir := filepath.Join(cloneRoot, "repo")
	cloneCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args := []string{"clone", "--depth", "1", "--filter=blob:none", "--single-branch"}
	if source.branch != "" {
		args = append(args, "--branch", source.branch)
	}
	args = append(args, "--", source.repository, cloneDir)
	output, err := exec.CommandContext(cloneCtx, "git", args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 4096 {
			message = message[len(message)-4096:]
		}
		return ChangeResult{}, fmt.Errorf("clone Skill repository: %w: %s", err, message)
	}

	candidates := make([]string, 0, 3)
	if source.subpath != "" {
		candidates = append(candidates, filepath.Join(cloneDir, source.subpath))
	}
	if request.Name != "" {
		candidates = append(candidates, filepath.Join(cloneDir, "skills", request.Name), filepath.Join(cloneDir, request.Name))
	}
	candidates = append(candidates, cloneDir)
	var packageDir string
	for _, candidate := range candidates {
		if info, statErr := os.Stat(filepath.Join(candidate, "SKILL.md")); statErr == nil && info.Mode().IsRegular() {
			packageDir = candidate
			break
		}
	}
	if packageDir == "" {
		return ChangeResult{}, fmt.Errorf("repository does not contain SKILL.md for %q", request.Name)
	}
	discovered, _, err := m.inspectPackage(packageDir, filepath.Base(packageDir), request.Scope, false)
	if err != nil {
		return ChangeResult{}, err
	}
	if request.Name != "" && discovered.info.Name != request.Name {
		return ChangeResult{}, fmt.Errorf("requested Skill %q, repository declared %q", request.Name, discovered.info.Name)
	}
	request.Action = ChangeImport
	request.SourcePath = packageDir
	return m.importSource(ctx, request, targetRoot)
}

func (m *FSManager) managedRoot(scope Scope, projectRoot string) (string, error) {
	switch scope {
	case ScopeGlobalManaged:
		return m.globalManagedDir, nil
	case ScopeProjectManaged:
		if strings.TrimSpace(projectRoot) == "" {
			return "", errors.New("project_root is required for project Skill changes")
		}
		return filepath.Join(projectRoot, ".p-chat", "skills"), nil
	default:
		return "", fmt.Errorf("scope %q is read-only; import into a managed scope", scope)
	}
}

type skillImportBudget struct {
	maxFiles int64
	maxBytes int64
	files    int64
	bytes    int64
}

func (b *skillImportBudget) consume(size int64) error {
	b.files++
	b.bytes += size
	if b.files > b.maxFiles || b.bytes > b.maxBytes {
		return errors.New("Skill import exceeds configured safety limits")
	}
	return nil
}

func (m *FSManager) copyPackage(ctx context.Context, source, target string, budget *skillImportBudget) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Skill package contains unsupported symlink: %s", path)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve Skill package path %s: %w", path, err)
		}
		if !samePath(path, resolved) {
			return fmt.Errorf("Skill package contains unsupported link: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Skill package contains unsupported non-regular file: %s", path)
		}
		return m.copyRegularFile(path, destination, info, budget)
	})
}

func (m *FSManager) copyRegularFile(path, destination string, observedInfo os.FileInfo, budget *skillImportBudget) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	openedInfo, err := in.Stat()
	if err != nil {
		_ = in.Close()
		return err
	}
	resolvedAfterOpen, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(path, resolvedAfterOpen) || !openedInfo.Mode().IsRegular() || !os.SameFile(observedInfo, openedInfo) {
		_ = in.Close()
		return fmt.Errorf("Skill package file changed or became linked while importing: %s", path)
	}
	if openedInfo.Size() > m.maxFileBytes {
		_ = in.Close()
		return errors.New("Skill import exceeds configured safety limits")
	}
	if err := budget.consume(openedInfo.Size()); err != nil {
		_ = in.Close()
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, copyErr := io.CopyN(out, in, openedInfo.Size())
	inCloseErr := in.Close()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if inCloseErr != nil {
		return inCloseErr
	}
	return closeErr
}

func ensureDirectChild(root, child string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	if err != nil || rel == "." || filepath.Dir(rel) != "." {
		return fmt.Errorf("Skill target %q is outside managed root %q", child, root)
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Skill != diagnostics[j].Skill {
			return diagnostics[i].Skill < diagnostics[j].Skill
		}
		if diagnostics[i].Code != diagnostics[j].Code {
			return diagnostics[i].Code < diagnostics[j].Code
		}
		return diagnostics[i].Source < diagnostics[j].Source
	})
}

// BuildCatalogContext 创建放入静态提示词的有界发现索引。
// BuildCatalogContext creates the bounded discovery index placed in the static prompt.
func BuildCatalogContext(catalog Catalog, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 8 * 1024
	}
	var builder strings.Builder
	builder.WriteString("## Available Skills\n\n")
	builder.WriteString("Use the `skill` tool with action `load` before following a Skill. Loading a Skill is reported to the user.\n\n")
	if len(catalog.Skills) == 0 {
		builder.WriteString("(none)\n")
		return builder.String()
	}
	for _, item := range catalog.Skills {
		line := fmt.Sprintf("- `%s` [%s]: %s\n", item.Name, item.Scope, item.Description)
		if builder.Len()+len(line) > maxBytes {
			builder.WriteString("- (Skill index truncated; call `skill` with action `list`.)\n")
			break
		}
		builder.WriteString(line)
	}
	return builder.String()
}
