package agent

import (
	"fmt"

	"github.com/p-chat/pchat/internal/tool"
)

const subagentParentApprovalPrefix = "SUBAGENT_PARENT_APPROVAL_REQUIRED"

// subagentToolAuthorizationResult enforces the child-agent authority boundary.
// A sub-agent may gather project-local read-only evidence and public
// network requests (web_search / web_fetch GET or POST), but it must not
// own decisions that can mutate the workspace, run processes, reach
// private/internal addresses, or interact with the user. Those decisions
// are returned to the parent as a structured tool result instead of
// opening a child confirm flow.
func subagentToolAuthorizationResult(req ChatRequest, tc nativeToolCall, meta tool.Tool, sb sandboxForConfirm) (*tool.CallResult, bool) {
	if req.SubagentType == "" {
		return nil, false
	}

	if !tool.SubagentMayExpose(tc.Name) {
		policy := meta.EffectivePolicy()
		return subagentParentApprovalResult(tc.Name, fmt.Sprintf("%s/%s tool", policy.Category, policy.SideEffect), confirmTarget{}), true
	}

	if tc.Name == "todo_write" || tc.Name == "web_search" {
		return nil, false
	}

	if tc.Name == "web_fetch" {
		return subagentWebFetchAuthorization(tc)
	}

	if sb != nil {
		if target, ok := confirmTargetFor(tc.Name, tc.ArgsJSON, req.ProjectRoot, sb); ok {
			switch target.Decision {
			case tool.SandboxBlock:
				return subagentBlockedResult(tc.Name, target), true
			case tool.SandboxConfirm:
				return subagentParentApprovalResult(tc.Name, "sandbox confirmation required", target), true
			}
		}
	}

	policy := meta.EffectivePolicy()
	if policy.Category == tool.ToolCategoryRead && policy.SideEffect == tool.ToolSideEffectNone {
		return nil, false
	}

	return subagentParentApprovalResult(tc.Name, fmt.Sprintf("%s/%s tool", policy.Category, policy.SideEffect), confirmTarget{}), true
}

func subagentWebFetchAuthorization(tc nativeToolCall) (*tool.CallResult, bool) {
	fetchURL, _, _, err := tool.DecodeWebFetchArgs([]byte(tc.ArgsJSON))
	if err != nil {
		return nil, false
	}
	if fetchURL == "" {
		return nil, false
	}
	if err := tool.PublicWebFetchURLError(fetchURL); err != nil {
		return subagentBlockedResult(tc.Name, confirmTarget{Reason: err.Error()}), true
	}
	return nil, false
}

func subagentParentApprovalResult(toolName, reason string, target confirmTarget) *tool.CallResult {
	content := fmt.Sprintf("%s: tool %q was not executed. Sub-agents cannot approve or run this operation directly; return a concise request to the parent conversation explaining why it is needed.", subagentParentApprovalPrefix, toolName)
	if reason != "" {
		content += "\nreason: " + reason
	}
	if target.ResolvedPath != "" {
		content += "\nresolved_path: " + target.ResolvedPath
	}
	if target.PathClass != "" {
		content += "\npath_class: " + target.PathClass
	}
	return &tool.CallResult{
		Content:      content,
		IsError:      true,
		Status:       tool.CallStatusWaiting,
		Summary:      "Needs parent approval: " + toolName,
		RequiresUser: true,
		NextAction:   "ask_parent",
	}
}

func subagentBlockedResult(toolName string, target confirmTarget) *tool.CallResult {
	content := fmt.Sprintf("E_SANDBOX: sub-agent tool %q blocked by sandbox policy", toolName)
	if target.Reason != "" {
		content += "\nreason: " + target.Reason
	}
	if target.ResolvedPath != "" {
		content += "\nresolved_path: " + target.ResolvedPath
	}
	return &tool.CallResult{
		Content: content,
		IsError: true,
		Status:  tool.CallStatusBlocked,
		Summary: "Blocked by sandbox: " + toolName,
	}
}
