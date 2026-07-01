package subagent

// Markdown agent loader. Supports `.p-chat/agent/*.md` files with
// YAML frontmatter and a body that becomes the agent's system
// prompt. Mirrors opencode's
// `packages/opencode/src/config/agent.ts:load()` and Claude Code's
// `src/utils/markdownConfigLoader.ts:loadMarkdownFilesForSubdir`.
//
// File path conventions (in load order, last-wins):
//
//  1. ~/.p-chat/agent/*.md           (global user agents)
//  2. <project>/.p-chat/agent/*.md   (per-project agents)
//
// Both directories are optional. Missing directories are silently
// skipped — the loader never errors out for an absent path.
//
// Frontmatter schema (YAML):
//
//	---
//	name: my-agent                       # required; agent identifier
//	description: Short "when to use" hint # required; surfaced to parent LLM
//	model: openai/gpt-4o-mini            # optional; "providerID/modelID"
//	color: "#44BA81"                     # optional; hex or CSS color
//	tools: [read_file, list_files]        # optional; per-agent whitelist
//	hidden: false                        # optional; exclude from description
//	---
//	Body of file = the agent's system prompt.
//
// The loader is intentionally permissive: unknown frontmatter keys
// are ignored (with a warning), missing optional fields use
// defaults, and a missing body means "inherit the parent's
// prompt". This mirrors how opencode's ConfigAgentV1 parses.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadFromDir reads all `*.md` files in `dir` and returns the
// parsed AgentInfo records. The directory must already exist; an
// empty slice is returned if it does not. Subdirectories are NOT
// recursed (opencode supports nesting via slash-separated names;
// we keep the flat layout for v1 — easier to audit, easier to
// document).
func LoadFromDir(dir string) ([]AgentInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent dir %q: %w", dir, err)
	}
	var out []AgentInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)
		a, err := ParseAgentFile(path)
		if err != nil {
			// Don't abort the whole load for one bad file —
			// surface the error and continue. The CLI / GUI
			// logs the list of failed parses so the user can
			// fix them.
			fmt.Fprintf(os.Stderr, "[subagent] failed to parse %s: %v\n", path, err)
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// ParseAgentFile reads one .md file and returns the parsed
// AgentInfo. Format:
//
//	---
//	<yaml frontmatter>
//	---
//	<body = system prompt>
//
// The YAML frontmatter parser is a small purpose-built scanner
// (not a full YAML library) that handles the subset we need:
// flat key/value pairs with `[]string` and string values. Adding
// a real YAML library would inflate the binary for one config
// format we control — we can swap in gopkg.in/yaml.v3 later if
// users need nested values.
func ParseAgentFile(path string) (AgentInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return AgentInfo{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // up to 1 MB

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return AgentInfo{}, fmt.Errorf("read: %w", err)
	}

	// Extract frontmatter.
	var (
		fmLines []string
		body    string
	)
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return AgentInfo{}, fmt.Errorf("unterminated frontmatter (missing closing ---)")
		}
		fmLines = lines[1:end]
		body = strings.Join(lines[end+1:], "\n")
	}

	// Parse frontmatter into a flat key→value map first, then
	// project onto AgentInfo. The flat map makes "unknown key"
	// warnings easy and keeps the YAML subset tiny.
	fm, err := parseFlatFrontmatter(fmLines)
	if err != nil {
		return AgentInfo{}, fmt.Errorf("frontmatter: %w", err)
	}

	// The agent name defaults to the file's stem so users can
	// write a .md file without a `name:` line and still have
	// it work.
	stem := strings.TrimSuffix(filepath.Base(path), ".md")

	a := AgentInfo{
		Name:        fm.string("name", stem),
		Description: fm.string("description", ""),
		Prompt:      strings.TrimSpace(body),
		Model:       fm.string("model", ""),
		Color:       fm.string("color", ""),
		Hidden:      fm.bool("hidden", false),
		Tools:       fm.strings("tools"),
		Source:      path,
	}

	// Validate.
	if strings.TrimSpace(a.Name) == "" {
		return AgentInfo{}, fmt.Errorf("missing 'name' (and stem is empty)")
	}
	if strings.TrimSpace(a.Description) == "" {
		return AgentInfo{}, fmt.Errorf("missing 'description' (required so the parent LLM knows when to use this agent)")
	}
	// Source tag: mark as user-defined.
	if a.Source != "" {
		a.Builtin = false
	}
	return a, nil
}

// flatFM is a tiny frontmatter key→value store. Values can be
// string, []string, or bool. Used by ParseAgentFile.
type flatFM map[string]any

func (f flatFM) string(key, def string) string {
	if v, ok := f[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func (f flatFM) bool(key string, def bool) bool {
	if v, ok := f[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
		if s, ok := v.(string); ok {
			// Lenient: accept "true"/"false" as strings.
			switch strings.ToLower(s) {
			case "true", "yes", "1":
				return true
			case "false", "no", "0":
				return false
			}
		}
	}
	return def
}

func (f flatFM) strings(key string) []string {
	v, ok := f[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case string:
		// Lenient: "tools: [a, b, c]" gets tokenized.
		s := strings.TrimSpace(t)
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// parseFlatFrontmatter is a deliberately minimal YAML frontmatter
// parser. It handles:
//
//   - `key: value`                (string, with/without quotes)
//   - `key: [a, b, c]`            (string list, inline)
//   - `key: true | false`         (bool)
//   - `key:`                      (empty → nil; treated as "")
//   - `# ...`                     (comment, ignored)
//
// It does NOT handle nested maps, block lists, anchors, or
// multi-line strings. If a user needs those, the file is rejected
// with a clear error suggesting the supported subset.
func parseFlatFrontmatter(lines []string) (flatFM, error) {
	out := make(flatFM)
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			return nil, fmt.Errorf("line %d: expected 'key: value' (no colon)", i+1)
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		// Quote stripping.
		val = unquote(val)

		// Type detection.
		if val == "" {
			out[key] = ""
			continue
		}
		if val == "true" || val == "false" {
			out[key] = val == "true"
			continue
		}
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			inner := val[1 : len(val)-1]
			parts := strings.Split(inner, ",")
			arr := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(unquote(p))
				if p != "" {
					arr = append(arr, p)
				}
			}
			out[key] = arr
			continue
		}
		// Treat everything else as a string.
		out[key] = val
	}
	return out, nil
}

// unquote strips a single layer of " or ' wrapping if both ends
// match. Leaves the string untouched otherwise (so unquoted
// values flow through unchanged).
func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' || first == '\'') && first == last {
			return s[1 : len(s)-1]
		}
	}
	return s
}
