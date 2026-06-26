package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/style"
	"golang.org/x/term"
)

// InputLine reads a line of input with inline slash-command autocomplete.
// Suggestions are shown as dimmed "ghost text" appended to the current input,
// never as a separate popup below. This avoids layout corruption in the
// chat box when the user types `/`.
//
// Returns the input string and whether it was a slash command.
func InputLine(s style.Style, provider string) (string, bool, error) {
	fd := int(os.Stdin.Fd())

	// Check if terminal supports raw mode
	if !term.IsTerminal(fd) {
		var input string
		fmt.Scanln(&input)
		return input, strings.HasPrefix(input, "/"), nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		var input string
		fmt.Scanln(&input)
		return input, strings.HasPrefix(input, "/"), nil
	}
	defer term.Restore(fd, oldState)

	// Print prompt
	printPromptRaw(s, provider)

	var buf []byte
	var ghost string // current ghost suggestion (empty = none)
	var ghostIdx int // -1 = no match selected
	var matches []string

	// showGhost clears the current line, re-prints the prompt + buffer +
	// dimmed ghost text. The cursor stays right after `buf` so the user
	// can keep typing and the ghost updates live.
	showGhost := func() {
		dim := color.New(color.FgHiBlack)
		fmt.Print("\r\033[K")
		printPromptRaw(s, provider)
		fmt.Print(string(buf))
		if ghost != "" && ghostIdx >= 0 && ghostIdx < len(matches) {
			// Print the suffix of the current match that is not already in buf.
			matched := matches[ghostIdx]
			if strings.HasPrefix(matched, string(buf)) && len(matched) > len(buf) {
				dim.Print(matched[len(buf):])
			}
		}
	}

	// acceptGhost replaces the buffer with the current match and clears the
	// ghost. Returns true if a match was applied.
	acceptGhost := func() bool {
		if ghost != "" && ghostIdx >= 0 && ghostIdx < len(matches) {
			buf = []byte(matches[ghostIdx])
			ghost = ""
			ghostIdx = -1
			showGhost()
			return true
		}
		return false
	}

	// readRune reads a complete UTF-8 rune from stdin
	readRune := func() (rune, int, error) {
		var first [1]byte
		n, err := os.Stdin.Read(first[:])
		if err != nil || n == 0 {
			return 0, 0, err
		}

		b := first[0]
		var size int
		switch {
		case b < 0x80:
			size = 1
		case b < 0xC0:
			size = 1 // invalid start byte
		case b < 0xE0:
			size = 2
		case b < 0xF0:
			size = 3
		default:
			size = 4
		}

		if size == 1 {
			return rune(b), 1, nil
		}

		rest := make([]byte, size-1)
		read := 0
		for read < size-1 {
			nr, err := os.Stdin.Read(rest[read:])
			if err != nil {
				return 0, read + 1, err
			}
			read += nr
		}

		all := append([]byte{b}, rest...)
		r, _ := decodeRune(all)
		return r, size, nil
	}

	// refreshMatches recomputes the match list and the ghost suggestion
	// based on the current buffer.
	refreshMatches := func() {
		ghost = ""
		ghostIdx = -1
		matches = nil
		if strings.HasPrefix(string(buf), "/") {
			matches = getSlashSuggestions(string(buf))
			if len(matches) > 0 {
				ghost = matches[0]
				ghostIdx = 0
			}
		}
	}

	for {
		r, _, err := readRune()
		if err != nil {
			break
		}

		chBytes := []byte(string(r))
		ch := chBytes[0]

		// Enter
		if ch == '\r' || ch == '\n' {
			// If a ghost is visible and matches the prefix exactly, accept it
			// (so pressing Enter on `/h` fills in `/help` and submits).
			if acceptGhost() {
				// fall through to submit with the completed buffer
			}
			fmt.Println()
			input := string(buf)
			return input, strings.HasPrefix(input, "/"), nil
		}

		// Ctrl+C
		if ch == 3 {
			fmt.Println()
			return "", false, fmt.Errorf("interrupted")
		}

		// Ctrl+U - clear line
		if ch == 21 {
			buf = buf[:0]
			refreshMatches()
			showGhost()
			continue
		}

		// Ctrl+A / Ctrl+E - move to start / end of input
		if ch == 1 || ch == 5 {
			continue
		}

		// Backspace
		if ch == 127 || ch == 8 {
			if len(buf) > 0 {
				buf = removeLastRune(buf)
			}
			refreshMatches()
			showGhost()
			continue
		}

		// Tab - accept ghost (no cycling; keeps things predictable)
		if ch == '\t' {
			if acceptGhost() {
				// ghost accepted, recompute next ghost for continuation
				refreshMatches()
				showGhost()
			}
			continue
		}

		// Right arrow - accept ghost
		if ch == 0x1b {
			seq := make([]byte, 2)
			if _, err := os.Stdin.Read(seq); err != nil {
				continue
			}
			if seq[0] == '[' {
				switch seq[1] {
				case 'C': // Right - accept ghost
					if acceptGhost() {
						refreshMatches()
						showGhost()
					}
				case 'D': // Left - ignore (cursor move inside buf not supported in this minimal impl)
					// no-op
				case 'A': // Up - previous match
					if len(matches) > 0 {
						ghostIdx = (ghostIdx - 1 + len(matches)) % len(matches)
						ghost = matches[ghostIdx]
						showGhost()
					}
				case 'B': // Down - next match
					if len(matches) > 0 {
						ghostIdx = (ghostIdx + 1) % len(matches)
						ghost = matches[ghostIdx]
						showGhost()
					}
				}
			}
			continue
		}

		// Regular character (could be multi-byte)
		buf = append(buf, chBytes...)
		refreshMatches()
		showGhost()
	}

	return string(buf), strings.HasPrefix(string(buf), "/"), nil
}

// decodeRune decodes the first valid UTF-8 rune from b
func decodeRune(b []byte) (rune, int) {
	for i := 1; i <= len(b) && i <= 4; i++ {
		r, size := decodeRunePartial(b[:i])
		if size > 0 {
			return r, size
		}
	}
	return 0, 0
}

func decodeRunePartial(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	switch {
	case c < 0x80:
		return rune(c), 1
	case c < 0xC0:
		return 0, 0
	case c < 0xE0:
		if len(b) < 2 {
			return 0, 0
		}
		return rune(c&0x1F)<<6 | rune(b[1]&0x3F), 2
	case c < 0xF0:
		if len(b) < 3 {
			return 0, 0
		}
		return rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F), 3
	default:
		if len(b) < 4 {
			return 0, 0
		}
		return rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F), 4
	}
}

// removeLastRune removes the last UTF-8 rune from buf
func removeLastRune(buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}
	i := len(buf) - 1
	for i > 0 && (buf[i]&0xC0) == 0x80 {
		i--
	}
	return buf[:i]
}

func getSlashSuggestions(input string) []string {
	var matches []string
	for _, cmd := range slashCmds {
		if strings.HasPrefix(cmd.Name, input) {
			matches = append(matches, cmd.Name)
		}
		for _, alias := range cmd.Aliases {
			if strings.HasPrefix(alias, input) {
				matches = append(matches, alias)
			}
		}
	}
	return matches
}

func printPromptRaw(s style.Style, provider string) {
	var icon string
	switch s {
	case style.Cute:
		icon = "🐹"
	case style.Guofeng:
		icon = "📜"
	case style.Tech:
		icon = "⚡"
	default:
		icon = "❯"
	}
	fmt.Printf("  %s \033[90m[%s]\033[0m ", icon, provider)
}
