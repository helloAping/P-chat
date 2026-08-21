package agent

import "fmt"

const (
	subagentProgressReminderFirst = 50
	subagentProgressReminderEvery = 50
)

// buildSubagentGuardPrompt is appended only for child ChatRequests. It
// preserves the selected sub-agent prompt (built-in, custom, or inherited)
// while adding a shared execution contract.
func buildSubagentGuardPrompt(subType, taskID string) string {
	if subType == "" {
		return ""
	}
	idLine := ""
	if taskID != "" {
		idLine = fmt.Sprintf("\nTask id: %s.", taskID)
	}
	return fmt.Sprintf(`---

## Sub-Agent Execution Contract

You are running as sub-agent %s.%s The parent agent will only see your final answer, not a full transcript.

Rules:
- Stay inside the assigned sub-task. Do not expand scope, redesign the parent task, or pursue unrelated improvements.
- Every tool call must directly advance the assigned sub-task. If a tool path fails twice, switch strategy instead of retrying variants.
- Keep a compact private checklist of what has been confirmed, what remains, and what assumption you are making.
- Prefer forward progress over exhaustive wandering: once you have enough evidence for the requested answer, stop tool use and return the answer.
- Do not ask the user questions. If the task is ambiguous, make the smallest reasonable assumption and state it in the final response.
- Do not include raw tool transcripts or internal reasoning in the final response. Return concise findings, evidence, remaining uncertainty, and any recommended next action.
- If interrupted, return the best partial result rather than an empty answer.`, subType, idLine)
}

func subagentProgressReminderDue(round int) bool {
	return round >= subagentProgressReminderFirst &&
		(round-subagentProgressReminderFirst)%subagentProgressReminderEvery == 0
}

func buildSubagentProgressReminder(req ChatRequest, round int) string {
	taskID := req.SubagentTaskID
	if taskID == "" {
		taskID = "(none)"
	}
	return fmt.Sprintf(`Sub-agent progress check at round %d.

You are still inside sub-agent %s (task_id: %s). Before any further tool call:
1. Re-state the narrow sub-task to yourself.
2. Identify the smallest remaining evidence/action needed.
3. Stop repeating already-tested paths. If the next tool call does not directly reduce uncertainty, return a partial or final answer now.

Continue only if the next tool call is necessary for the assigned sub-task.`, round, req.SubagentType, taskID)
}
