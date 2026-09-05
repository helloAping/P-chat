package tool

import (
	"strings"
	"time"
)

// ToolCategory classifies the kind of work a tool performs.
type ToolCategory string

const (
	ToolCategoryRead          ToolCategory = "read"
	ToolCategoryMutate        ToolCategory = "mutate"
	ToolCategoryExec          ToolCategory = "exec"
	ToolCategoryInteractive   ToolCategory = "interactive"
	ToolCategoryExternal      ToolCategory = "external"
	ToolCategoryCheckpoint    ToolCategory = "checkpoint"
	ToolCategoryOrchestration ToolCategory = "orchestration"
)

// ToolSideEffect describes the resource a tool can change or access.
type ToolSideEffect string

const (
	ToolSideEffectNone      ToolSideEffect = "none"
	ToolSideEffectWorkspace ToolSideEffect = "workspace"
	ToolSideEffectProcess   ToolSideEffect = "process"
	ToolSideEffectNetwork   ToolSideEffect = "network"
	ToolSideEffectState     ToolSideEffect = "state"
)

// ToolRisk is the default authorization level for a tool call.
type ToolRisk string

const (
	ToolRiskLow     ToolRisk = "low"
	ToolRiskMedium  ToolRisk = "medium"
	ToolRiskConfirm ToolRisk = "confirm"
	ToolRiskHigh    ToolRisk = "high"
)

// ToolParallelism controls whether calls may run concurrently.
type ToolParallelism string

const (
	ToolParallelSafe      ToolParallelism = "safe"
	ToolParallelSerial    ToolParallelism = "same_target_serial"
	ToolParallelExclusive ToolParallelism = "exclusive"
)

// ToolPolicy is execution metadata used by the scheduler and UI. It is kept
// separate from the LLM JSON schema so prompts do not need to learn runtime
// implementation details.
type ToolPolicy struct {
	Category             ToolCategory    `json:"category,omitempty"`
	SideEffect           ToolSideEffect  `json:"side_effect,omitempty"`
	Risk                 ToolRisk        `json:"risk,omitempty"`
	Parallelism          ToolParallelism `json:"parallelism,omitempty"`
	TimeoutMS            int             `json:"timeout_ms,omitempty"`
	MaxOutputBytes       int             `json:"max_output_bytes,omitempty"`
	RequiresVerification bool            `json:"requires_verification,omitempty"`
	Idempotent           bool            `json:"idempotent,omitempty"`
}

// EffectivePolicy returns a complete, conservative policy for a tool. A
// registered policy overrides inferred fields while omitted fields retain the
// safe default for the tool name.
func (t Tool) EffectivePolicy() ToolPolicy {
	p := defaultToolPolicy(t.Name)
	if t.Policy == nil {
		return p
	}
	custom := *t.Policy
	if custom.Category != "" {
		p.Category = custom.Category
	}
	if custom.SideEffect != "" {
		p.SideEffect = custom.SideEffect
	}
	if custom.Risk != "" {
		p.Risk = custom.Risk
	}
	if custom.Parallelism != "" {
		p.Parallelism = custom.Parallelism
	}
	if custom.TimeoutMS != 0 {
		p.TimeoutMS = custom.TimeoutMS
	}
	if custom.MaxOutputBytes > 0 {
		p.MaxOutputBytes = custom.MaxOutputBytes
	}
	if custom.RequiresVerification {
		p.RequiresVerification = true
	}
	if custom.Idempotent {
		p.Idempotent = true
	}
	return p
}

// Timeout returns the bounded per-call timeout represented by the policy.
// A negative TimeoutMS means "no per-tool deadline"; callers should still
// pass a cancellable parent context so user stop / turn cancellation works.
func (p ToolPolicy) Timeout() time.Duration {
	if p.TimeoutMS < 0 {
		return 0
	}
	if p.TimeoutMS <= 0 {
		return DefaultToolTimeout
	}
	return time.Duration(p.TimeoutMS) * time.Millisecond
}

// CanRunInParallel reports whether the tool is safe to batch with another
// call. The default is deliberately conservative for unknown tools.
func (p ToolPolicy) CanRunInParallel() bool {
	if p.Parallelism != ToolParallelSafe {
		return false
	}
	if p.Category == ToolCategoryRead && p.SideEffect == ToolSideEffectNone {
		return true
	}
	return p.Category == ToolCategoryOrchestration && p.SideEffect == ToolSideEffectProcess
}

// SubagentMayExpose reports whether a child agent may see (and, after
// per-call checks, run) this tool. Visibility and the execution gate
// share this predicate so a sub-agent cannot discover a tool it is
// not allowed to execute.
//
// Allowed: project-local read tools, image_recognize, private todo_write,
// web_search, and web_fetch (GET/POST to public URLs). Write/exec/
// interactive/browser/MCP/dynamic tools stay with the parent conversation.
func SubagentMayExpose(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "read_attachment" || name == "media_recognize" {
		return false
	}
	switch name {
	case "todo_write", "web_search", "web_fetch", "image_recognize":
		return true
	}
	p := defaultToolPolicy(name)
	return p.Category == ToolCategoryRead && p.SideEffect == ToolSideEffectNone
}

func defaultToolPolicy(name string) ToolPolicy {
	name = strings.ToLower(strings.TrimSpace(name))
	p := ToolPolicy{
		Category:       ToolCategoryOrchestration,
		SideEffect:     ToolSideEffectProcess,
		Risk:           ToolRiskConfirm,
		Parallelism:    ToolParallelExclusive,
		TimeoutMS:      durationMS(DefaultToolTimeout),
		MaxOutputBytes: 1 << 20,
	}
	switch {
	case name == "read_file" || name == "read_attachment" || name == "read_docx" || name == "read_pdf" || name == "list_files" || name == "grep" || name == "recall" || name == "wiki_lookup" || name == "wiki_list" || name == "task_status" || name == "image_recognize" || name == "media_recognize":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryRead, ToolSideEffectNone, ToolRiskLow, ToolParallelSafe
		p.TimeoutMS = durationMS(ReadToolTimeout)
	case name == "write_file" || name == "edit_file":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryMutate, ToolSideEffectWorkspace, ToolRiskConfirm, ToolParallelExclusive
		p.TimeoutMS, p.RequiresVerification = durationMS(WriteToolTimeout), true
	case name == "exec_command" || name == "start_process" || name == "stop_process" || name == "read_process_output" || name == "list_processes":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryExec, ToolSideEffectProcess, ToolRiskHigh, ToolParallelExclusive
		p.TimeoutMS, p.RequiresVerification = durationMS(DefaultToolTimeout), true
	case name == "todo_write":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryCheckpoint, ToolSideEffectState, ToolRiskLow, ToolParallelExclusive
		p.TimeoutMS, p.Idempotent = durationMS(CheckpointToolTimeout), true
	case name == "question":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryInteractive, ToolSideEffectNone, ToolRiskMedium, ToolParallelExclusive
		p.TimeoutMS = durationMS(QuestionWaitTimeout)
	case name == "web_search":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryExternal, ToolSideEffectNetwork, ToolRiskMedium, ToolParallelExclusive
		p.TimeoutMS = durationMS(WebSearchTimeout)
	case name == "web_fetch" || strings.HasPrefix(name, "browser_"):
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryExternal, ToolSideEffectNetwork, ToolRiskMedium, ToolParallelExclusive
		p.TimeoutMS = durationMS(WebFetchTimeout)
	case name == "task":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryOrchestration, ToolSideEffectProcess, ToolRiskConfirm, ToolParallelSafe
		p.TimeoutMS = -1
	case name == "task_wait":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryOrchestration, ToolSideEffectNone, ToolRiskLow, ToolParallelExclusive
		p.TimeoutMS = -1
	case name == "task_cancel":
		p.Category, p.SideEffect, p.Risk, p.Parallelism = ToolCategoryOrchestration, ToolSideEffectState, ToolRiskMedium, ToolParallelExclusive
		p.TimeoutMS = durationMS(CheckpointToolTimeout)
	}
	return p
}

func durationMS(d time.Duration) int {
	return int(d / time.Millisecond)
}
