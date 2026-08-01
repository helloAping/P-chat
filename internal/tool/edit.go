package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type editFileArgs struct {
	Path       string `json:"path"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

// handleEditFile replaces an exact text fragment in one text file.
// By default it requires exactly one match so a stale or underspecified
// edit cannot silently modify multiple locations.
func handleEditFile(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a editFileArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Path == "" {
		return &CallResult{Content: "path is required", IsError: true}, nil
	}
	if a.OldText == "" {
		return &CallResult{Content: "old_text is required and cannot be empty", IsError: true}, nil
	}
	select {
	case <-ctx.Done():
		return &CallResult{Content: "edit cancelled", IsError: true}, ctx.Err()
	default:
	}

	a.Path = resolveToProjectRoot(ctx, a.Path)
	if sb := sandboxFromCtx(ctx); sb != nil && sb.CheckWriteDecision(a.Path, projectRootFromCtx(ctx)) == SandboxBlock {
		return &CallResult{
			Content: fmt.Sprintf("E_SANDBOX: edit blocked by sandbox policy\n  path: %s", a.Path),
			IsError: true,
		}, nil
	}
	if isInUploadDir(a.Path) {
		return &CallResult{
			Content: fmt.Sprintf("E_UPLOAD_DIR: edit blocked for chat upload path %s", a.Path),
			IsError: true,
		}, nil
	}

	data, err := os.ReadFile(a.Path)
	if err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	content := string(data)
	matches := strings.Count(content, a.OldText)
	if matches == 0 {
		return &CallResult{Content: "old_text was not found in the target file", IsError: true}, nil
	}
	if matches > 1 && !a.ReplaceAll {
		return &CallResult{
			Content: fmt.Sprintf("old_text matched %d times; provide more context or set replace_all=true", matches),
			IsError: true,
		}, nil
	}

	replacements := 1
	updated := strings.Replace(content, a.OldText, a.NewText, 1)
	if a.ReplaceAll {
		replacements = matches
		updated = strings.ReplaceAll(content, a.OldText, a.NewText)
	}
	if a.DryRun {
		content := fmt.Sprintf(
			"[dry-run] would edit: %s\nreplacements: %d\nsize: %d -> %d bytes",
			a.Path, replacements, len(content), len(updated),
		)
		return &CallResult{Content: content, Summary: "Dry run: would edit " + a.Path, NextAction: "review"}, nil
	}
	select {
	case <-ctx.Done():
		return &CallResult{Content: "edit cancelled", IsError: true}, ctx.Err()
	default:
	}
	if err := writeFile(a.Path, []byte(updated)); err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{
		Content:      fmt.Sprintf("edited %s (%d replacement)", a.Path, replacements),
		Summary:      fmt.Sprintf("Edited %d replacement in %s", replacements, a.Path),
		ChangedPaths: []string{a.Path},
		NextAction:   "verify",
	}, nil
}
