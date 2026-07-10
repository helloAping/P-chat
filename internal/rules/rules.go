package rules

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/paths"
)

type Rule struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	Path     string `json:"path"`
	// IsGlobal is true for rules loaded from the global
	// (~/.p-chat/rules) directory, false for rules from the
	// project directory. Set by LoadAll; not exported in the
	// JSON shape so the on-disk structure is unchanged.
	IsGlobal bool `json:"-"`
}

// LoadAll loads rules from both global and project directories.
// Project rules are appended after global rules.
// Output is sorted by name for deterministic ordering (LLM cache stability).
//
// Deprecated: use LoadAllWithRoot so the project-level
// directory is resolved against the session's projectRoot
// rather than the server's CWD. LoadAll is preserved for
// the CLI commands (pchat rules list) that run before any
// session is selected.
func LoadAll() ([]Rule, error) {
	return LoadAllWithRoot("")
}

// LoadAllWithRoot is the 2026-07 project-aware loader.
//
// Merge policy (AND, both levels always loaded, project
// rules appended after global):
//   - global:  ~/.p-chat/rules/
//   - project: <root>/.p-chat/rules/    (per session root)
//
// When root is empty (no project pinned), the project slot
// is skipped and only the global slot is consulted. The
// pre-2026-07 LoadAll delegated to paths.ProjectRulesDir()
// which used os.Getwd() — same bug as the skill loader:
// broken for the Wails GUI where the server's CWD is
// unrelated to the user's project.
func LoadAllWithRoot(root string) ([]Rule, error) {
	var rules []Rule

	// Load global rules
	globalRules, _ := loadFromDir(paths.GlobalRulesDir())
	for _, r := range globalRules {
		r.IsGlobal = true
		rules = append(rules, r)
	}

	// Load project rules (appended). Use
	// ProjectRulesDirWithRoot so the path is anchored to
	// the session's projectRoot, not the server's CWD.
	if root != "" {
		projectRules, _ := loadFromDir(paths.ProjectRulesDirWithRoot(root))
		for _, r := range projectRules {
			r.IsGlobal = false
			rules = append(rules, r)
		}
	}

	// Sort by IsGlobal (global < project) then by name for a
	// stable, byte-identical output across calls. The LLM's
	// prefix cache keys on byte equality, so non-deterministic
	// order causes cache misses.
	//
	// The previous version used strings.Contains(Path, "global")
	// which was fragile (a user-named directory containing
	// "global" would be mis-categorised). The explicit
	// IsGlobal field is set by loadFromDir based on which
	// directory the rule was loaded from.
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].IsGlobal != rules[j].IsGlobal {
			return rules[i].IsGlobal
		}
		return rules[i].Name < rules[j].Name
	})
	return rules, nil
}

func loadFromDir(dir string) ([]Rule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var rules []Rule
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		ruleName := strings.TrimSuffix(name, ".md")
		rules = append(rules, Rule{
			Name:    ruleName,
			Content: string(data),
			Path:    path,
		})
	}
	return rules, nil
}

// BuildRulesContext builds the rules context for system prompt.
// Output is byte-stable: the section is always present, even when there are
// no rules (so the resulting system prompt is identical between calls when
// no rules change).
func BuildRulesContext(rules []Rule) string {
	var sb strings.Builder
	sb.WriteString("## Rules\n\n")
	if len(rules) == 0 {
		sb.WriteString("(none)\n")
		return sb.String()
	}
	sb.WriteString("You must follow these rules:\n\n")
	for _, r := range rules {
		sb.WriteString("### ")
		sb.WriteString(r.Name)
		sb.WriteString("\n")
		sb.WriteString(r.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// Watch polls the global and project rules directories for
// changes and invokes onChange whenever a file's mtime changes.
// The watcher is best-effort: it polls every pollInterval and
// ignores transient errors (e.g. file briefly locked by an
// editor on save). onChange is called from a dedicated goroutine.
//
// 2026-07: the project-level dir is now resolved against
// the session's projectRoot via
// paths.ProjectRulesDirWithRoot(root). Pass root == "" to
// watch only the global dir (matches the no-project-pinned
// case).
//
// Returns a stop function that the caller should defer.
//
// This replaces the previous "restart the server to pick up
// rule changes" behavior so users can iterate on rules without
// a full restart.
func Watch(onChange func(), pollInterval time.Duration, root string) (stop func()) {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	dirs := []string{paths.GlobalRulesDir()}
	if root != "" {
		dirs = append(dirs, paths.ProjectRulesDirWithRoot(root))
	} else {
		// Pre-2026-07 behaviour for empty root: fall back
		// to the CWD-based project dir so the watcher
		// still catches files in the (now deprecated) CWD-
		// anchored project location. The agent's own
		// ReloadWithRootIfChanged will re-target the
		// watcher on the next project switch.
		dirs = append(dirs, paths.ProjectRulesDir())
	}
	lastMtimes := make(map[string]time.Time)

	// Prime: capture initial mtimes so the first run doesn't
	// fire onChange for files that haven't changed.
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			p := filepath.Join(d, e.Name())
			info, err := e.Info()
			if err != nil {
				continue
			}
			lastMtimes[p] = info.ModTime()
		}
	}

	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				changed := false
				currentMtimes := make(map[string]time.Time)
				seen := make(map[string]bool)
				for _, d := range dirs {
					entries, err := os.ReadDir(d)
					if err != nil {
						continue
					}
					for _, e := range entries {
						if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
							continue
						}
						p := filepath.Join(d, e.Name())
						seen[p] = true
						info, err := e.Info()
						if err != nil {
							continue
						}
						mt := info.ModTime()
						currentMtimes[p] = mt
						if last, ok := lastMtimes[p]; !ok || !mt.Equal(last) {
							changed = true
						}
					}
				}
				// Detect deletions: files in lastMtimes not in current.
				for p := range lastMtimes {
					if !seen[p] {
						changed = true
					}
				}
				lastMtimes = currentMtimes
				if changed && onChange != nil {
					log.Printf("[rules] change detected, reloading")
					func() {
						defer func() { _ = recover() }()
						onChange()
					}()
				}
			}
		}
	}()
	return func() {
		close(stopped)
		<-done
	}
}
