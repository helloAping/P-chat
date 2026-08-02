package agent

import "github.com/p-chat/pchat/internal/llm"

// ToolPhase identifies the protocol phase that controls which tools the LLM
// may call in a round.
type ToolPhase string

const (
	ToolPhaseWork       ToolPhase = "work"
	ToolPhasePlan       ToolPhase = "plan"
	ToolPhaseCheckpoint ToolPhase = "checkpoint"
	ToolPhaseReconcile  ToolPhase = "todo_reconcile"
)

func toolPhaseForRound(planMode bool, checkpoint todoCheckpointState) ToolPhase {
	if planMode {
		return ToolPhasePlan
	}
	if checkpoint == todoCheckpointReconcile {
		return ToolPhaseReconcile
	}
	if checkpoint == todoCheckpointReview {
		return ToolPhaseCheckpoint
	}
	return ToolPhaseWork
}

func toolDefsForPhase(defs []llm.ToolDef, phase ToolPhase) []llm.ToolDef {
	if phase != ToolPhasePlan && phase != ToolPhaseCheckpoint && phase != ToolPhaseReconcile {
		return defs
	}
	if phase == ToolPhaseReconcile {
		for _, def := range defs {
			if def.Name == "todo_write" {
				return []llm.ToolDef{def}
			}
		}
		return nil
	}
	return todoOnlyToolDefs(defs)
}

func toolAllowedInPhase(name string, phase ToolPhase) bool {
	if phase == ToolPhaseReconcile {
		return name == "todo_write"
	}
	if phase != ToolPhasePlan && phase != ToolPhaseCheckpoint {
		return true
	}
	return isTodoCheckpointTool(name)
}
