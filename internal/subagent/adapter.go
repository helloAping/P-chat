package subagent

import "github.com/p-chat/pchat/internal/agent"

// SubagentRegistryAdapter exposes a *Registry via the
// agent.SubagentRegistry interface. Built in main.go so the
// subagent package doesn't need to import the agent package
// (which would create an import cycle: agent -> subagent ->
// agent via the SubagentInfo type).
//
// The adapter is a one-line shim that maps the agent-package
// view (SubagentInfo) to the subagent-package view (AgentInfo).
// Used by the `task` tool to look up sub-agent metadata from
// inside the agent's tool dispatcher.
type SubagentRegistryAdapter struct{ R *Registry }

// Get implements agent.SubagentRegistry.
func (a SubagentRegistryAdapter) Get(name string) (agent.SubagentInfo, bool) {
	if a.R == nil {
		return agent.SubagentInfo{}, false
	}
	ai, ok := a.R.Get(name)
	if !ok {
		return agent.SubagentInfo{}, false
	}
	return agent.SubagentInfo{
		Name:        ai.Name,
		Description: ai.Description,
		Prompt:      ai.Prompt,
		Model:       ai.Model,
		Color:       ai.Color,
		Tools:       ai.Tools,
	}, true
}

// List implements agent.SubagentRegistry.
func (a SubagentRegistryAdapter) List() []agent.SubagentInfo {
	if a.R == nil {
		return nil
	}
	src := a.R.List()
	out := make([]agent.SubagentInfo, 0, len(src))
	for _, ai := range src {
		out = append(out, agent.SubagentInfo{
			Name:        ai.Name,
			Description: ai.Description,
			Prompt:      ai.Prompt,
			Model:       ai.Model,
			Color:       ai.Color,
			Tools:       ai.Tools,
		})
	}
	return out
}
