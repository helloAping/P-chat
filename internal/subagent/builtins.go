package subagent

// Built-in sub-agents that ship with P-Chat. Mirrors opencode's
// `packages/opencode/src/agent/agent.ts:140-265` set of native
// agents (general, explore, plan) and Claude Code's
// `src/tools/AgentTool/builtInAgents.ts:22-71` (general-purpose,
// Explore, Plan).
//
// The four design choices we make vs. the upstream tools:
//
//  1. We keep the agent prompts **short** (a paragraph or two).
//     opencode's prompts are 18-44 lines of "guidelines"; Claude
//     Code's are 200+ lines. The P-Chat parent already has rich
//     style + AGENTS + rules + skills context, so the sub-agent
//     prompt is an overlay, not a complete instruction set.
//
//  2. We **enumerate tool whitelists** instead of using the opencode
//     "deny everything then allow" idiom. The P-Chat tool registry
//     is small (a dozen names), so an explicit whitelist is easier
//     to audit and translate to /agents.
//
//  3. We **don't ship a `compaction` or `title` agent** — those are
//     opencode-internal session features (history compaction, title
//     generation) and P-Chat handles those internally. If we need
//     them later, add them with Hidden: true.
//
//  4. We **don't ship a `statusline-setup` agent** like Claude Code —
//     P-Chat has no PS1 statusline. Skip.

// generalPurposePrompt is the default prompt used when the parent
// LLM spawns a sub-agent without a specialized subagent_type. It
// inherits the parent's tool set, model, and base prompt — the
// only thing it adds is the explicit "focus on the task" framing,
// which helps the LLM treat the child as a dedicated worker
// rather than a continuation of the parent turn.
//
// Backticks inside the prompt are represented as a Unicode
// backtick-like character (U+2018 / U+2019) to avoid colliding
// with Go's raw-string delimiter. The text reads fine in the LLM
// prompt.
const generalPurposePrompt = "You are a focused sub-agent spawned by a parent agent to handle a single, well-defined sub-task.\n\n" +
	"You have your own context (no shared history with the parent), your own tool set, and run independently. The parent will see only your final text response.\n\n" +
	"When working on your assigned task:\n" +
	"- Be direct and concise. The parent already has the high-level picture.\n" +
	"- Do not call the 'task' tool yourself — sub-agents cannot spawn sub-agents.\n" +
	"- Do not ask the user clarifying questions; if the prompt is ambiguous, return your best effort and explain the assumption you made.\n" +
	"- When you are done, return a single text response that answers the parent's question. Do not include tool-call transcripts, internal reasoning, or 'I will now…' preambles."

// explorePrompt is a read-only file search specialist. Mirrors
// opencode's explore and Claude Code's Explore agent. Restricted
// to read-only tools; cannot edit, write, or execute commands.
const explorePrompt = "You are a file search specialist. You excel at thoroughly navigating and exploring codebases.\n\n" +
	"Your strengths:\n" +
	"- Rapidly finding files using glob patterns\n" +
	"- Searching code and text with powerful regex\n" +
	"- Reading and analyzing file contents\n\n" +
	"Guidelines:\n" +
	"- Use list_files for directory contents and read_file for individual files.\n" +
	"- Use exec_command for grep/find/ls/git status/git log/git diff/cat/head/tail — read-only shell commands only. Do not modify any file or run any command that changes system state.\n" +
	"- Adapt your search approach based on the thoroughness level specified by the caller.\n" +
	"- Return file paths as absolute paths in your final response.\n" +
	"- For clear communication, avoid using emojis.\n\n" +
	"Complete the user's search request efficiently and report your findings clearly. Do not create any files."

// planPrompt is a read-only architect that produces implementation
// plans. Mirrors opencode's plan and Claude Code's Plan agent.
// Same tool restrictions as Explore; the difference is the prompt
// framing (produce a plan, not just answers).
const planPrompt = "You are a read-only architect agent. Your job is to produce a clear, actionable implementation plan for the user's request.\n\n" +
	"You are READ-ONLY:\n" +
	"- You can list files, read files, and run read-only shell commands (grep, find, ls, git status, git log, git diff, cat, head, tail).\n" +
	"- You CANNOT edit, write, create, delete, or modify any file.\n" +
	"- You CANNOT run commands that change system state.\n" +
	"- You CANNOT call the 'task' tool or spawn sub-agents.\n\n" +
	"When producing a plan:\n" +
	"1. First, gather enough context to understand the current state. Search the codebase, read the relevant files, and form a clear picture of what exists.\n" +
	"2. Then, write a step-by-step plan that another agent (or a human) can execute.\n" +
	"3. Structure the plan as a numbered list of concrete steps. Each step should name specific files to touch and the change to make.\n" +
	"4. End with a 'Critical Files for Implementation' section listing the 3-5 files that will be created or modified.\n" +
	"5. Do NOT execute the plan. Do not write any code. Just return the plan as your final text response."

// Builtins returns the built-in agent catalog. The order is
// stable: general-purpose is registered first so the parent's
// "I want a sub-agent but I don't know which" fallback is
// natural.
//
// All built-ins are marked Builtin: true and Source: "builtin"
// so the GUI can show a "built-in" badge and the loader can
// skip these names in user-defined .md files.
func Builtins() []AgentInfo {
	return []AgentInfo{
		{
			Name:        "general-purpose",
			Description: "General-purpose agent for multi-step tasks. Inherits the parent's tools and model. Use this when no specialized agent fits.",
			Prompt:      generalPurposePrompt,
			Color:       "#5B9BD5",
			Builtin:     true,
			Source:      "builtin",
		},
		{
			Name:        "explore",
			Description: "Fast read-only agent specialized for exploring codebases. Use for searches, file lookups, and codebase Q&A. Cannot modify files.",
			Prompt:      explorePrompt,
			Color:       "#44BA81",
			Tools:       []string{"read_file", "list_files", "exec_command"},
			Builtin:     true,
			Source:      "builtin",
		},
		{
			Name:        "plan",
			Description: "Read-only architect that produces a step-by-step implementation plan. Use when you need a design before executing.",
			Prompt:      planPrompt,
			Color:       "#E8A33D",
			Tools:       []string{"read_file", "list_files", "exec_command"},
			Builtin:     true,
			Source:      "builtin",
		},
	}
}
