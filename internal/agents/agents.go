package agents

import (
	"errors"
	"log"
	"os"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

// Candidate 描述指令加载器检查的一个 AGENTS.md 候选位置。
// Candidate describes one AGENTS.md slot considered by the instruction
// loader. Selected is true only for the non-empty readable slot that wins
// the loader's precedence order.
type Candidate struct {
	Slot     string
	Label    string
	Path     string
	Exists   bool
	Selected bool
}

// Inspection 描述候选文件以及实际选中的提示词。
// Inspection describes the exact AGENTS.md candidates and prompt selected
// for a project root. Prompt is the same string returned by LoadAllWithRoot.
type Inspection struct {
	Candidates []Candidate
	Prompt     string
}

// readFile is a tiny helper that reads a file, treats a
// missing file as empty, and logs any other error so a
// permission problem or a corrupt file isn't silently
// ignored. The previous version returned "" for ALL errors,
// meaning a permission-denied or a corrupted file would just
// look like "no AGENTS.md configured" with no diagnostic.
func readFile(path string) string {
	content, _ := readCandidate(path)
	return content
}

func readCandidate(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		log.Printf("[agents] WARN: failed to read %s: %v", path, err)
		return "", true
	}
	return strings.TrimRight(string(data), "\n"), true
}

// LoadGlobal loads ~/.p-chat/AGENTS.md
func LoadGlobal() string {
	return readFile(paths.GlobalAgents())
}

// LoadProject loads ./.p-chat/AGENTS.md (project root)
func LoadProject() string {
	return readFile(paths.ProjectAgents())
}

// LoadProjectWithRoot loads AGENTS.md from an explicit project root.
func LoadProjectWithRoot(root string) string {
	return readFile(paths.ProjectAgentsWithRoot(root))
}

// LoadAll loads both global and project AGENTS.md and returns combined content.
// Output is byte-stable: the section is always present, even when both files
// are missing, so the resulting system prompt is identical between calls.
func LoadAll() string {
	return LoadAllWithRoot("")
}

// LoadAllWithRoot loads the AGENTS.md instruction file for the
// current session, applying the 2026-07 OR policy:
//
//  1. <root>/AGENTS.md           (project root, primary)
//  2. <root>/.p-chat/AGENTS.md   (project .p-chat, fallback)
//  3. ~/.p-chat/AGENTS.md        (global, last resort)
//
// The first non-empty readable file wins; the others are ignored.
// Output is byte-stable: the section is always present, with
// a (none) marker when nothing was found, so the resulting
// system prompt is identical between calls when nothing
// changes (LLM prefix cache invariant).
//
// The section header varies based on which slot was hit so
// the LLM can attribute the instructions correctly:
//
//	### Project (root)   — slot 1
//	### Project (.p-chat) — slot 2
//	### Global           — slot 3
//
// Switching projectRoot may change the header — the
// agentsSignatureWithRoot in agent.go incorporates the mtime
// of all three files plus the root, so the static-prompt
// cache key changes automatically and the LLM sees a
// consistent prompt after a project switch.
//
// The previous design loaded BOTH global and project in
// parallel, which produced conflicting instructions when
// the user had different rules at the two levels (e.g. a
// "always answer in English" project rule vs. a "default
// to Chinese" global rule). The OR strategy picks one
// source per session so there's never a conflict.
//
// When root is empty (no project pinned), the two
// project-level slots are skipped and the global slot
// alone is used. This matches the "global mode" UX where
// the user is intentionally working without a project.
func LoadAllWithRoot(root string) string {
	return InspectWithRoot(root).Prompt
}

// InspectWithRoot 按实际提示词加载顺序解析 AGENTS.md，同时保留候选元数据。
// InspectWithRoot resolves AGENTS.md with the same OR precedence used for
// the agent prompt, while retaining candidate metadata for diagnostics.
func InspectWithRoot(root string) Inspection {
	var sb strings.Builder
	sb.WriteString("## Agent Instructions\n\n")

	// Slot 1: <root>/AGENTS.md (project root, primary)
	// Slot 2: <root>/.p-chat/AGENTS.md (project .p-chat, fallback)
	// Slot 3: ~/.p-chat/AGENTS.md (global, last resort)
	type slot struct {
		slot   string
		label  string
		header string
		path   string
	}
	slots := make([]slot, 0, 3)
	if root != "" {
		slots = append(slots,
			slot{slot: "project_root", label: "项目根", header: "### Project (root)", path: paths.ProjectAgentsWithRoot(root)},
			slot{slot: "project_pchat", label: "项目 .p-chat", header: "### Project (.p-chat)", path: paths.ProjectPChatAgentsWithRoot(root)},
		)
	}
	slots = append(slots, slot{slot: "global", label: "全局", header: "### Global", path: paths.GlobalAgents()})

	result := Inspection{Candidates: make([]Candidate, 0, len(slots))}
	var found slot
	var foundContent string
	for _, candidate := range slots {
		content := ""
		exists := false
		if foundContent == "" {
			content, exists = readCandidate(candidate.path)
		} else {
			// 胜出后只检查低优先级文件是否存在，避免在每轮提示词加载时读取其全文。
			// Once a slot wins, inspect only metadata of lower-priority files.
			_, err := os.Stat(candidate.path)
			exists = err == nil || !errors.Is(err, os.ErrNotExist)
		}
		selected := foundContent == "" && content != ""
		result.Candidates = append(result.Candidates, Candidate{
			Slot: candidate.slot, Label: candidate.label, Path: candidate.path, Exists: exists, Selected: selected,
		})
		if selected {
			found = candidate
			foundContent = content
		}
	}
	if foundContent == "" {
		sb.WriteString("(none)\n")
		result.Prompt = sb.String()
		return result
	}
	sb.WriteString(found.header)
	sb.WriteString("\n\n")
	sb.WriteString(foundContent)
	sb.WriteString("\n\n")
	result.Prompt = sb.String()
	return result
}
