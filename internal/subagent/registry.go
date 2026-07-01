// Package subagent — built-in and user-defined sub-agent catalog.
//
// P-Chat's `task` tool lets the parent LLM spawn a focused sub-agent
// to handle a multi-step sub-task in parallel or in isolation. The
// available sub-agents are described by AgentInfo records: a name
// the LLM passes via `subagent_type`, a one-line description that
// helps the LLM pick the right one, a system-prompt override
// (empty = inherit the parent's), an optional model override, an
// optional tool whitelist (empty = inherit the parent's filtered
// set), and an accent color for the UI card.
//
// Three sources are merged into a single Registry at startup:
//
//  1. Built-in agents: general-purpose, explore, plan
//     (defined as Go constants in builtins.go).
//  2. User-defined agents in ~/.p-chat/agent/*.md (YAML frontmatter
//     + body), loaded by loadFromDir in markdown.go.
//  3. Per-project agents in <project>/.p-chat/agent/*.md,
//     overlaid on top of (2) (project wins on name collision).
//
// The Registry is the read-only view the `task` tool needs. It
// implements agent.SubagentRegistry (defined in internal/agent)
// so the tool can stay decoupled from this package.
//
// File path conventions mirror opencode's
// packages/opencode/src/config/agent.ts:load() and
// packages/app/src/pages/session/composer/session-composer-state.ts.
package subagent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// AgentInfo is the catalog record for one sub-agent. It is the
// shared view used by (a) the `task` tool to look up a child
// agent, (b) the dynamic tool description that lists available
// agents to the parent LLM, (c) the CLI `/tools` and `/agents`
// views, and (d) the Wails GUI agent management panel.
//
// Fields map 1:1 to YAML frontmatter keys in .p-chat/agent/*.md
// (see markdown.go) and to the same-named fields in opencode's
// ConfigAgentV1.Info and Claude Code's BaseAgentDefinition.
type AgentInfo struct {
	// Name is the unique agent identifier (e.g. "explore",
	// "general-purpose"). The parent LLM passes this via the
	// `subagent_type` argument of the `task` tool.
	Name string `json:"name"`
	// Description is a one-line "when to use" hint. Appended to
	// the `task` tool's description so the parent LLM can pick
	// the right agent without external docs. Required.
	Description string `json:"description"`
	// Prompt is the agent's full system prompt. If empty, the
	// child inherits the parent's system prompt (style +
	// AGENTS + rules + skills). If non-empty, it REPLACES the
	// parent's prompt — the child becomes a specialized
	// persona. The body of a .md file becomes the prompt.
	Prompt string `json:"prompt,omitempty"`
	// Model is an optional "providerID/modelID" override. If
	// empty, the child uses the parent's model.
	Model string `json:"model,omitempty"`
	// Color is the agent's accent color. Accepts "#RRGGBB" or
	// any CSS color name. Used to tint the SubAgentCard.
	Color string `json:"color,omitempty"`
	// Tools is an optional per-agent tool whitelist. If
	// non-empty, only these tools are exposed to the child
	// (in addition to the `task`/`recall` exclusion). An empty
	// slice means "inherit the parent's filter".
	Tools []string `json:"tools,omitempty"`
	// Builtin marks agents compiled into the binary so the
	// loader can skip them in the markdown directory and the
	// UI can render a "built-in" badge. Not serialized.
	Builtin bool `json:"-"`
	// Hidden excludes the agent from the `task` tool's
	// description listing. Internal agents (compaction,
	// title, etc.) set this to true.
	Hidden bool `json:"hidden,omitempty"`
	// Source is a human-readable path to the file that
	// defined this agent (e.g. "builtin", "~/.p-chat/agent/foo.md").
	// Used by `/agents` and the GUI to show provenance.
	Source string `json:"source,omitempty"`
}

// Registry is the read-only sub-agent catalog. It is safe for
// concurrent reads; the loader takes an exclusive lock during
// build and the tool dispatcher only ever calls Get/List.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]AgentInfo
}

// NewRegistry returns an empty registry. Use RegisterAll or
// MergeFrom to populate it.
func NewRegistry() *Registry {
	return &Registry{agents: make(map[string]AgentInfo)}
}

// Register adds (or replaces) one agent. The caller is
// responsible for uniqueness — duplicate names silently
// overwrite.
func (r *Registry) Register(a AgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[a.Name] = a
}

// RegisterAll adds several agents in one critical section.
func (r *Registry) RegisterAll(as []AgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range as {
		r.agents[a.Name] = a
	}
}

// MergeFrom layers another registry's entries on top of this one.
// Entries with matching names are overwritten. Used to overlay
// per-project agents on top of global agents.
func (r *Registry) MergeFrom(other *Registry) {
	if other == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	other.mu.RLock()
	defer other.mu.RUnlock()
	for k, v := range other.agents {
		r.agents[k] = v
	}
}

// Get returns the named agent and a found flag.
func (r *Registry) Get(name string) (AgentInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[name]
	return a, ok
}

// List returns a stable, sorted view of all non-hidden agents.
// Built-in sort: by name ascending. Used by the dynamic tool
// description and the `/agents` command.
func (r *Registry) List() []AgentInfo {
	return r.ListWithFilter(nil)
}

// ListWithFilter returns agents matching the predicate. Pass nil
// for "all". The result is always sorted by name.
func (r *Registry) ListWithFilter(p func(AgentInfo) bool) []AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentInfo, 0, len(r.agents))
	for _, a := range r.agents {
		if p != nil && !p(a) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Describe returns a one-line "name: description" string for each
// non-hidden agent. Used to build the dynamic description of the
// `task` tool so the parent LLM knows what agents exist.
//
// Format (mirrors opencode's registry.ts:describeTask):
//
//	- explore: Fast agent specialized for exploring codebases.
//	- plan: Read-only architect that produces implementation plans.
//	- general-purpose: General-purpose agent for multi-step tasks.
func (r *Registry) Describe() string {
	agents := r.ListWithFilter(func(a AgentInfo) bool { return !a.Hidden })
	if len(agents) == 0 {
		return ""
	}
	lines := make([]string, 0, len(agents))
	for _, a := range agents {
		lines = append(lines, fmt.Sprintf("- %s: %s", a.Name, a.Description))
	}
	return "Available agent types:\n" + strings.Join(lines, "\n")
}
