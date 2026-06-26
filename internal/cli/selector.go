package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"golang.org/x/term"
)

// SelectOption represents a selectable option
type SelectOption struct {
	Label string
	Value string
}

// Select shows an interactive selection list and returns the selected index.
// Supports arrow keys (up/down) and Enter to select.
//
// On Enter, the rendered selection list is cleared and the function returns
// silently. The caller is responsible for any confirmation message.
func Select(prompt string, options []SelectOption) (int, error) {
	if len(options) == 0 {
		return -1, fmt.Errorf("no options")
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		// Fallback to number input
		return selectFallback(prompt, options)
	}
	defer term.Restore(fd, oldState)

	cursor := 0
	printed := 0

	// Initial render
	printed = renderSelector(prompt, options, cursor, 0)

	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			break
		}

		// Enter
		if buf[0] == '\r' || buf[0] == '\n' {
			clearLines(printed)
			return cursor, nil
		}

		// Escape sequence (arrow keys)
		if buf[0] == 0x1b && n >= 3 && buf[1] == '[' {
			switch buf[2] {
			case 'A': // Up
				if cursor > 0 {
					cursor--
				}
			case 'B': // Down
				if cursor < len(options)-1 {
					cursor++
				}
			case 'C': // Right - treat as down
				if cursor < len(options)-1 {
					cursor++
				}
			case 'D': // Left - treat as up
				if cursor > 0 {
					cursor--
				}
			}
		}

		// Tab - cycle forward
		if buf[0] == '\t' {
			cursor = (cursor + 1) % len(options)
		}

		// Ctrl+C
		if buf[0] == 3 {
			clearLines(printed)
			return -1, fmt.Errorf("cancelled")
		}

		// Re-render
		clearLines(printed)
		printed = renderSelector(prompt, options, cursor, 0)
	}

	return -1, fmt.Errorf("no input")
}

func renderSelector(prompt string, options []SelectOption, cursor int, offset int) int {
	cyan := color.New(color.FgCyan)
	white := color.New(color.FgWhite)
	hiBlack := color.New(color.FgHiBlack)
	green := color.New(color.FgGreen)

	cyan.Printf("  %s\n", prompt)
	lines := 1 // prompt line

	for i, opt := range options[offset:] {
		idx := i + offset
		if idx == cursor {
			green.Printf("  → %s\n", opt.Label)
		} else {
			hiBlack.Printf("    %s\n", opt.Label)
		}
		lines++
		if i >= 7 { // Max 8 options
			break
		}
	}

	white.Println("  ─────────────────────────────────────")
	hiBlack.Println("  ↑↓ 选择  Enter 确认  Tab 下一个")
	lines += 2

	return lines
}

func clearLines(n int) {
	for i := 0; i < n; i++ {
		fmt.Print("\033[A\033[K")
	}
}

func selectFallback(prompt string, options []SelectOption) (int, error) {
	cyan := color.New(color.FgCyan)
	cyan.Printf("  %s\n", prompt)

	for i, opt := range options {
		fmt.Printf("    %d. %s\n", i+1, opt.Label)
	}
	fmt.Print("  请选择序号: ")

	var input string
	fmt.Scanln(&input)

	for i, opt := range options {
		if fmt.Sprintf("%d", i+1) == input || strings.EqualFold(opt.Value, input) {
			return i, nil
		}
	}

	return -1, fmt.Errorf("invalid selection")
}

// SelectWithDefault shows a selection with a default value pre-selected.
// On Enter, the rendered list is cleared and the selected index is returned.
// The caller is responsible for any confirmation message.
func SelectWithDefault(prompt string, options []SelectOption, defaultVal string) (int, error) {
	defaultIdx := 0
	for i, opt := range options {
		if opt.Value == defaultVal {
			defaultIdx = i
			break
		}
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return selectFallback(prompt, options)
	}
	defer term.Restore(fd, oldState)

	cursor := defaultIdx
	printed := 0
	printed = renderSelector(prompt, options, cursor, 0)

	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			break
		}

		if buf[0] == '\r' || buf[0] == '\n' {
			clearLines(printed)
			return cursor, nil
		}

		if buf[0] == 0x1b && n >= 3 && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				if cursor > 0 {
					cursor--
				}
			case 'B':
				if cursor < len(options)-1 {
					cursor++
				}
			case 'C':
				if cursor < len(options)-1 {
					cursor++
				}
			case 'D':
				if cursor > 0 {
					cursor--
				}
			}
		}

		if buf[0] == '\t' {
			cursor = (cursor + 1) % len(options)
		}

		if buf[0] == 3 {
			clearLines(printed)
			return -1, fmt.Errorf("cancelled")
		}

		clearLines(printed)
		printed = renderSelector(prompt, options, cursor, 0)
	}

	return -1, fmt.Errorf("no input")
}

// Confirm shows a yes/no confirmation with arrow keys
func Confirm(prompt string, defaultYes bool) (bool, error) {
	options := []SelectOption{
		{Label: "是 (Yes)", Value: "yes"},
		{Label: "否 (No)", Value: "no"},
	}
	defaultIdx := 1
	if defaultYes {
		defaultIdx = 0
	}

	idx, err := SelectWithDefault(prompt, options, options[defaultIdx].Value)
	if err != nil {
		return false, err
	}
	return idx == 0, nil
}
