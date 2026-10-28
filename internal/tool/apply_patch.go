package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// apply_patch applies multi-file, multi-hunk edits in one call.
// Prefer this over repeated edit_file / write_file for coordinated changes.
//
// Two input forms are accepted:
//  1. Codex-style text in "patch":
//     *** Begin Patch
//     *** Update File: path
//     @@
//      context
//     -old
//     +new
//     *** Add File: path
//     +content
//     *** Delete File: path
//     *** End Patch
//  2. Structured "operations" for exact old_text/new_text hunks.
type applyPatchArgs struct {
	Patch      string           `json:"patch,omitempty"`
	Operations []patchOperation `json:"operations,omitempty"`
	DryRun     bool             `json:"dry_run,omitempty"`
}

type patchOperation struct {
	Op      string      `json:"op"` // update | add | delete
	Path    string      `json:"path"`
	Content string      `json:"content,omitempty"` // add
	Hunks   []patchHunk `json:"hunks,omitempty"`   // update
}

type patchHunk struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type parsedPatchFile struct {
	Op      string // update | add | delete
	Path    string
	Content string      // add
	Hunks   []patchHunk // update
}

// handleApplyPatch applies a multi-file patch under sandbox write rules.
func handleApplyPatch(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a applyPatchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	files, err := normalizePatchArgs(a)
	if err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	if len(files) == 0 {
		return &CallResult{Content: "patch is empty: provide patch text or operations", IsError: true}, nil
	}

	select {
	case <-ctx.Done():
		return &CallResult{Content: "apply_patch cancelled", IsError: true}, ctx.Err()
	default:
	}

	projectRoot := projectRootFromCtx(ctx)
	sb := sandboxFromCtx(ctx)
	var changed []string
	var reports []string

	for i, file := range files {
		select {
		case <-ctx.Done():
			return &CallResult{Content: "apply_patch cancelled", IsError: true}, ctx.Err()
		default:
		}
		path := resolveToProjectRoot(ctx, file.Path)
		file.Path = path
		if isInUploadDir(path) {
			return &CallResult{
				Content: fmt.Sprintf("E_UPLOAD_DIR: apply_patch blocked for chat upload path %s", path),
				IsError: true,
			}, nil
		}
		if sb != nil && sb.CheckWriteDecision(path, projectRoot) == SandboxBlock {
			return &CallResult{
				Content: fmt.Sprintf("E_SANDBOX: apply_patch blocked by sandbox policy\n  path: %s", path),
				IsError: true,
			}, nil
		}

		report, err := applyOnePatchFile(file, a.DryRun)
		if err != nil {
			ctxHint := conflictContext(file, err)
			return &CallResult{
				Content: fmt.Sprintf(
					"apply_patch failed on file %d/%d (%s %s):\n%s\n%s",
					i+1, len(files), file.Op, path, err.Error(), ctxHint,
				),
				IsError: true,
			}, nil
		}
		reports = append(reports, report)
		if !a.DryRun && file.Op != "delete" {
			changed = append(changed, path)
		} else if !a.DryRun && file.Op == "delete" {
			changed = append(changed, path)
		}
	}

	prefix := ""
	if a.DryRun {
		prefix = "[dry-run] "
	}
	summary := fmt.Sprintf("%sapplied %d file change(s)", prefix, len(files))
	return &CallResult{
		Content:      prefix + strings.Join(reports, "\n"),
		Summary:      summary,
		ChangedPaths: changed,
		NextAction:   "verify",
	}, nil
}

func normalizePatchArgs(a applyPatchArgs) ([]parsedPatchFile, error) {
	if len(a.Operations) > 0 {
		out := make([]parsedPatchFile, 0, len(a.Operations))
		for _, op := range a.Operations {
			opName := strings.ToLower(strings.TrimSpace(op.Op))
			if op.Path == "" {
				return nil, fmt.Errorf("operations entry missing path")
			}
			switch opName {
			case "update", "edit":
				if len(op.Hunks) == 0 {
					return nil, fmt.Errorf("update %s: hunks required", op.Path)
				}
				for _, h := range op.Hunks {
					if h.OldText == "" {
						return nil, fmt.Errorf("update %s: old_text cannot be empty", op.Path)
					}
				}
				out = append(out, parsedPatchFile{Op: "update", Path: op.Path, Hunks: op.Hunks})
			case "add", "create":
				out = append(out, parsedPatchFile{Op: "add", Path: op.Path, Content: op.Content})
			case "delete", "remove":
				out = append(out, parsedPatchFile{Op: "delete", Path: op.Path})
			default:
				return nil, fmt.Errorf("unknown op %q for %s (want update|add|delete)", op.Op, op.Path)
			}
		}
		return out, nil
	}
	return parseCodexPatch(a.Patch)
}

func applyOnePatchFile(file parsedPatchFile, dryRun bool) (string, error) {
	switch file.Op {
	case "add":
		if _, err := os.Stat(file.Path); err == nil {
			return "", fmt.Errorf("file already exists")
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if dryRun {
			return fmt.Sprintf("would add %s (%d bytes)", file.Path, len(file.Content)), nil
		}
		if err := writeFile(file.Path, []byte(file.Content)); err != nil {
			return "", err
		}
		return fmt.Sprintf("added %s (%d bytes)", file.Path, len(file.Content)), nil
	case "delete":
		if _, err := os.Stat(file.Path); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("file does not exist")
			}
			return "", err
		}
		if dryRun {
			return fmt.Sprintf("would delete %s", file.Path), nil
		}
		if err := os.Remove(file.Path); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted %s", file.Path), nil
	case "update":
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return "", err
		}
		content := string(data)
		original := content
		for hi, hunk := range file.Hunks {
			matches := strings.Count(content, hunk.OldText)
			if matches == 0 {
				return "", fmt.Errorf("hunk %d: old_text not found", hi+1)
			}
			if matches > 1 {
				return "", fmt.Errorf("hunk %d: old_text matched %d times; add more context", hi+1, matches)
			}
			content = strings.Replace(content, hunk.OldText, hunk.NewText, 1)
		}
		if content == original {
			return fmt.Sprintf("unchanged %s (hunks already applied)", file.Path), nil
		}
		if dryRun {
			return fmt.Sprintf("would update %s (%d hunks, %d -> %d bytes)", file.Path, len(file.Hunks), len(original), len(content)), nil
		}
		if err := writeFile(file.Path, []byte(content)); err != nil {
			return "", err
		}
		return fmt.Sprintf("updated %s (%d hunks)", file.Path, len(file.Hunks)), nil
	default:
		return "", fmt.Errorf("unknown op %q", file.Op)
	}
}

func conflictContext(file parsedPatchFile, err error) string {
	if file.Op != "update" || len(file.Hunks) == 0 {
		return ""
	}
	data, readErr := os.ReadFile(file.Path)
	if readErr != nil {
		return ""
	}
	content := string(data)
	var b strings.Builder
	b.WriteString("Conflict context (first failing hunk old_text preview):\n")
	for _, hunk := range file.Hunks {
		if strings.Count(content, hunk.OldText) == 1 {
			continue
		}
		preview := hunk.OldText
		if len(preview) > 240 {
			preview = preview[:240] + "…"
		}
		b.WriteString("--- expected ---\n")
		b.WriteString(preview)
		b.WriteString("\n--- file excerpt (first 400 chars) ---\n")
		excerpt := content
		if len(excerpt) > 400 {
			excerpt = excerpt[:400] + "…"
		}
		b.WriteString(excerpt)
		b.WriteString("\n")
		break
	}
	if b.Len() == 0 {
		b.WriteString(err.Error())
	}
	return b.String()
}

// parseCodexPatch parses the Codex / OpenAI apply_patch text envelope.
func parseCodexPatch(patch string) ([]parsedPatchFile, error) {
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return nil, fmt.Errorf("patch text is empty")
	}
	// Allow callers to omit Begin/End markers.
	if !strings.Contains(patch, "*** Begin Patch") {
		patch = "*** Begin Patch\n" + patch
	}
	if !strings.Contains(patch, "*** End Patch") {
		patch = patch + "\n*** End Patch"
	}

	lines := strings.Split(patch, "\n")
	var out []parsedPatchFile
	var cur *parsedPatchFile
	var hunkLines []string
	flushHunk := func() error {
		if cur == nil || len(hunkLines) == 0 {
			hunkLines = nil
			return nil
		}
		oldText, newText, err := hunkLinesToTexts(hunkLines)
		hunkLines = nil
		if err != nil {
			return err
		}
		if cur.Op == "add" {
			// Add File body uses + lines (or raw lines).
			cur.Content = newText
			if cur.Content == "" {
				cur.Content = oldText // tolerate unmarked body
			}
			return nil
		}
		if oldText == "" {
			return fmt.Errorf("%s: hunk old_text is empty (need context or - lines)", cur.Path)
		}
		cur.Hunks = append(cur.Hunks, patchHunk{OldText: oldText, NewText: newText})
		return nil
	}
	flushFile := func() error {
		if cur == nil {
			return nil
		}
		if err := flushHunk(); err != nil {
			return err
		}
		if cur.Op == "update" && len(cur.Hunks) == 0 {
			return fmt.Errorf("update %s: no hunks", cur.Path)
		}
		out = append(out, *cur)
		cur = nil
		return nil
	}

	inPatch := false
	for _, raw := range lines {
		line := raw
		trim := strings.TrimSpace(line)
		switch {
		case trim == "*** Begin Patch":
			inPatch = true
			continue
		case trim == "*** End Patch":
			if err := flushFile(); err != nil {
				return nil, err
			}
			inPatch = false
			continue
		}
		if !inPatch {
			continue
		}
		if strings.HasPrefix(trim, "*** Update File:") {
			if err := flushFile(); err != nil {
				return nil, err
			}
			path := strings.TrimSpace(strings.TrimPrefix(trim, "*** Update File:"))
			cur = &parsedPatchFile{Op: "update", Path: path}
			continue
		}
		if strings.HasPrefix(trim, "*** Add File:") {
			if err := flushFile(); err != nil {
				return nil, err
			}
			path := strings.TrimSpace(strings.TrimPrefix(trim, "*** Add File:"))
			cur = &parsedPatchFile{Op: "add", Path: path}
			continue
		}
		if strings.HasPrefix(trim, "*** Delete File:") {
			if err := flushFile(); err != nil {
				return nil, err
			}
			path := strings.TrimSpace(strings.TrimPrefix(trim, "*** Delete File:"))
			cur = &parsedPatchFile{Op: "delete", Path: path}
			if err := flushFile(); err != nil {
				return nil, err
			}
			continue
		}
		if cur == nil {
			continue
		}
		if trim == "@@" || strings.HasPrefix(trim, "@@ ") {
			if err := flushHunk(); err != nil {
				return nil, err
			}
			continue
		}
		hunkLines = append(hunkLines, line)
	}
	if err := flushFile(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no file operations found in patch")
	}
	return out, nil
}

func hunkLinesToTexts(lines []string) (oldText, newText string, err error) {
	var oldB, newB strings.Builder
	for _, line := range lines {
		if line == "" {
			// Blank line without marker: treat as context blank.
			oldB.WriteByte('\n')
			newB.WriteByte('\n')
			continue
		}
		switch line[0] {
		case ' ':
			oldB.WriteString(line[1:])
			oldB.WriteByte('\n')
			newB.WriteString(line[1:])
			newB.WriteByte('\n')
		case '-':
			oldB.WriteString(line[1:])
			oldB.WriteByte('\n')
		case '+':
			newB.WriteString(line[1:])
			newB.WriteByte('\n')
		case '\\':
			// "\ No newline at end of file" — ignore.
			continue
		default:
			// Unmarked line = context (tolerant of models omitting the space).
			oldB.WriteString(line)
			oldB.WriteByte('\n')
			newB.WriteString(line)
			newB.WriteByte('\n')
		}
	}
	return oldB.String(), newB.String(), nil
}

// PathsFromApplyPatchArgs extracts write targets for confirm UI / sandbox.
func PathsFromApplyPatchArgs(argsJSON string) []string {
	var a applyPatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return nil
	}
	files, err := normalizePatchArgs(a)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, f := range files {
		p := filepath.Clean(strings.TrimSpace(f.Path))
		if p == "" || p == "." {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
