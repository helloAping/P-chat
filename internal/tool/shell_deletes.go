package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

const maxShellDeleteSnapshots = 100

// snapshotShellDeletes records existing files a shell delete command is
// about to remove, so the current turn's change list shows those files
// instead of falling back to the previous turn.
func snapshotShellDeletes(ctx context.Context, command, workDir string) []string {
	targets := shellDeleteTargets(command, workDir)
	if len(targets) == 0 {
		return nil
	}
	recorded := make([]string, 0, len(targets))
	for _, path := range targets {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		RecordBeforeMutationFromCtx(ctx, "exec_command", path)
		recorded = append(recorded, path)
	}
	return recorded
}

func forgetUnchangedShellDeletes(ctx context.Context, paths []string) {
	j := journalFromCtx(ctx)
	if j == nil {
		return
	}
	j.ForgetUnchanged(SessionIDFromCtx(ctx), paths)
}

// shellDeleteTargets returns cleaned file paths named by del/rm/Remove-Item
// segments. Globs expand in their own directory. Non-delete segments are ignored.
func shellDeleteTargets(command, workDir string) []string {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		if len(out) >= maxShellDeleteSnapshots {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, segment := range splitShellSegments(command) {
		tokens := shellTokens(segment)
		if len(tokens) == 0 {
			continue
		}
		name, ok := deleteCommandName(tokens[0])
		if !ok {
			continue
		}
		skipNext := false
		for _, token := range tokens[1:] {
			if skipNext {
				skipNext = false
				continue
			}
			if isDeleteValueFlag(name, token) {
				if strings.EqualFold(strings.TrimLeft(token, "-"), "exclude") {
					skipNext = true
				}
				continue
			}
			if isDeleteFlag(name, token) {
				continue
			}
			for _, operand := range splitDeleteOperand(name, token) {
				for _, path := range expandDeleteOperand(workDir, operand) {
					add(path)
				}
			}
		}
	}
	return out
}

func splitShellSegments(command string) []string {
	var segs []string
	var b strings.Builder
	quote := byte(0)
	flush := func() {
		segment := strings.TrimSpace(b.String())
		b.Reset()
		if segment != "" {
			segs = append(segs, segment)
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			b.WriteByte(c)
			continue
		}
		if c == '\n' || c == ';' {
			flush()
			continue
		}
		if c == '&' || c == '|' {
			flush()
			for i+1 < len(command) && (command[i+1] == '&' || command[i+1] == '|') {
				i++
			}
			continue
		}
		b.WriteByte(c)
	}
	flush()
	return segs
}

func shellTokens(segment string) []string {
	var tokens []string
	var b strings.Builder
	quote := byte(0)
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tokens = append(tokens, b.String())
		b.Reset()
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if quote != 0 {
			if c == quote {
				quote = 0
				continue
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			flush()
			continue
		}
		b.WriteByte(c)
	}
	flush()
	return tokens
}

func deleteCommandName(token string) (string, bool) {
	base := token
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(strings.TrimSuffix(base, ".exe"))
	switch base {
	case "del", "erase", "rm", "unlink", "rmdir", "rd", "remove-item", "ri":
		return base, true
	default:
		return "", false
	}
}

func isDeleteFlag(cmd, token string) bool {
	if token == "" {
		return false
	}
	switch cmd {
	case "del", "erase", "rd", "rmdir":
		return strings.HasPrefix(token, "/")
	case "remove-item", "ri":
		return strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "-.")
	default:
		if strings.HasPrefix(token, "--") {
			return true
		}
		return strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "-.")
	}
}

func isDeleteValueFlag(cmd, token string) bool {
	if cmd != "remove-item" && cmd != "ri" {
		return false
	}
	switch strings.ToLower(strings.TrimLeft(token, "-")) {
	case "path", "literalpath", "include", "filter", "exclude", "erroraction", "ea", "confirm":
		return true
	default:
		return false
	}
}

func splitDeleteOperand(cmd, token string) []string {
	if cmd != "remove-item" && cmd != "ri" || !strings.Contains(token, ",") {
		return []string{token}
	}
	parts := strings.Split(token, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func expandDeleteOperand(workDir, token string) []string {
	token = strings.TrimSpace(token)
	if token == "" || token == "." || token == ".." {
		return nil
	}
	if strings.ContainsAny(token, "*?") {
		pattern := token
		if !filepath.IsAbs(pattern) && workDir != "" {
			pattern = filepath.Join(workDir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil
		}
		return matches
	}
	if filepath.IsAbs(token) || workDir == "" {
		return []string{filepath.Clean(token)}
	}
	return []string{filepath.Clean(filepath.Join(workDir, token))}
}
