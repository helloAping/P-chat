package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fatih/color"
)

// multilineEditor reads multi-line input from r until the user
// submits a terminator. The session is interactive — each keystroke
// is echoed to the user.
//
// Termination rules:
//   - A line containing only "." (dot)            → submit
//   - EOF (Ctrl+D on Unix, Ctrl+Z+Enter on Win)  → cancel
//   - A second "." in a row before any other line   → also a cancel
//     hint (we accept the first ".", so users can save by typing "."
//     if the plan has just one line; rare but harmless)
//
// Returns the joined lines (without the terminator) and a bool
// indicating whether the user confirmed (`true`) or cancelled
// (`false`).
func multilineEditor(in io.Reader, out io.Writer, prompt string, current string) (string, bool, error) {
	reader := bufio.NewReader(in)

	if prompt != "" {
		fmt.Fprintln(out, prompt)
	}
	if current != "" {
		// Print the existing plan as a comment so the user can
		// reference / copy from it.
		fmt.Fprintln(out)
		hi := color.New(color.FgHiBlack)
		hi.Fprintln(out, "── 原计划 (供参考, 可直接复用或整段重写) ──")
		for _, line := range strings.Split(current, "\n") {
			hi.Fprintf(out, "  # %s\n", line)
		}
		hi.Fprintln(out, "── ──────────────────── ──")
		fmt.Fprintln(out)
	}

	var lines []string
	for {
		fmt.Fprint(out, color.New(color.FgCyan).Sprint("  ▌ "))
		line, err := reader.ReadString('\n')
		// When err == io.EOF, `line` still holds any data buffered
		// before EOF. We must process it like a normal line, only
		// then bail out.
		if err != nil && err != io.EOF {
			return "", false, err
		}
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if line == "." {
				break
			}
			// Allow ".cancel" to bail out without saving.
			if line == ".cancel" {
				return "", false, nil
			}
			lines = append(lines, line)
		}
		if err == io.EOF {
			// EOF: commit whatever we have, or cancel on empty.
			if len(lines) == 0 {
				return "", false, nil
			}
			return strings.Join(lines, "\n"), true, nil
		}
	}
	if len(lines) == 0 {
		// User submitted an empty edit → no change.
		return current, true, nil
	}
	return strings.Join(lines, "\n"), true, nil
}

// readPlanDecision reads the y/n/e choice after a plan is shown.
// Replaces the inline bufio.NewReader in cmdPlan.
func readPlanDecision() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(color.New(color.FgMagenta).Sprint("  [y/n/e] > "))
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(line)), nil
}
