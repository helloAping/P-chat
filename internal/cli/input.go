package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/style"
	"golang.org/x/term"
)

// InputLine 读取一行输入，并在斜杠命令前缀下显示可筛选的命令弹窗。
// InputLine reads one input line and shows a filterable command popup for
// slash-command prefixes.
//
// Returns the input string and whether it was a slash command.
func InputLine(s style.Style, provider string, mode config.WorkMode) (string, bool, error) {
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
	printPromptRaw(s, provider, mode)

	var buf []byte
	var popup slashPopupState
	popupLines := 0
	popupAreaReserved := false
	popupWidth, popupHeight := slashPopupTerminalSize(fd, int(os.Stdout.Fd()), term.GetSize)
	popupMaxVisible := slashPopupDefaultVisible
	if popupHeight > 0 {
		available := popupHeight - 3 // input line + popup header/footer
		if available < popupMaxVisible {
			popupMaxVisible = available
		}
	}
	if popupMaxVisible < 0 {
		popupMaxVisible = 0
	}

	// redraw 清除旧弹窗并原地重绘输入与最新候选。
	// redraw clears the old popup and redraws the input plus current matches.
	redraw := func() {
		clearSlashPopup(os.Stdout, popupLines)
		fmt.Print("\r\033[2K")
		printPromptRaw(s, provider, mode)
		fmt.Print(string(buf))
		if popup.visible && popupMaxVisible > 0 && !popupAreaReserved {
			reserveSlashPopup(os.Stdout, popupMaxVisible+2)
			popupAreaReserved = true
			// 预留空间可能触发滚屏；重新绘制输入行以恢复准确光标位置。
			// Reserving space may scroll; redraw the input to restore its cursor.
			fmt.Print("\r\033[2K")
			printPromptRaw(s, provider, mode)
			fmt.Print(string(buf))
		}
		popupLines = renderSlashPopup(os.Stdout, popup, popupMaxVisible, popupWidth)
	}

	refreshPopup := func() {
		popup.refresh(string(buf))
	}

	acceptPopup := func() bool {
		command, ok := popup.accept()
		if !ok {
			return false
		}
		buf = []byte(command)
		redraw()
		return true
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

	for {
		r, _, err := readRune()
		if err != nil {
			break
		}

		chBytes := []byte(string(r))
		ch := chBytes[0]

		// Enter
		if ch == '\r' || ch == '\n' {
			// 弹窗可见时，Enter 接受当前项并立即执行。
			// When the popup is visible, Enter accepts and submits its selection.
			acceptPopup()
			clearSlashPopup(os.Stdout, popupLines)
			popupLines = 0
			fmt.Println()
			input := string(buf)
			return input, strings.HasPrefix(input, "/"), nil
		}

		// Ctrl+C
		if ch == 3 {
			clearSlashPopup(os.Stdout, popupLines)
			fmt.Println()
			return "", false, fmt.Errorf("interrupted")
		}

		// Ctrl+U - clear line
		if ch == 21 {
			buf = buf[:0]
			refreshPopup()
			redraw()
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
			refreshPopup()
			redraw()
			continue
		}

		// Tab - 接受当前候选。Tab accepts the selected suggestion.
		if ch == '\t' {
			acceptPopup()
			continue
		}

		// 方向键选择候选；右键接受。Arrow keys select; Right accepts.
		if ch == 0x1b {
			seq := make([]byte, 2)
			if _, err := io.ReadFull(os.Stdin, seq); err != nil {
				continue
			}
			if seq[0] == '[' {
				switch seq[1] {
				case 'C': // Right - accept selection
					acceptPopup()
				case 'D': // Left - ignore (cursor move inside buf not supported in this minimal impl)
					// no-op
				case 'A': // Up - previous match
					popup.move(-1)
					redraw()
				case 'B': // Down - next match
					popup.move(1)
					redraw()
				}
			}
			continue
		}

		// Regular character (could be multi-byte)
		buf = append(buf, chBytes...)
		refreshPopup()
		redraw()
	}

	clearSlashPopup(os.Stdout, popupLines)
	return string(buf), strings.HasPrefix(string(buf), "/"), nil
}

const (
	slashPopupDefaultVisible = 8
	slashPopupFallbackWidth  = 80
)

func slashPopupTerminalSize(inputFD, outputFD int, getSize func(int) (int, int, error)) (int, int) {
	for _, fd := range []int{outputFD, inputFD} {
		width, height, err := getSize(fd)
		if err == nil && width > 1 {
			// 最后一列留空，避免终端在满宽时自动折行。
			// Leave the last column unused to avoid automatic wrapping at full width.
			return width - 1, height
		}
	}
	return slashPopupFallbackWidth - 1, 0
}

type slashCommandSuggestion struct {
	Name        string
	Aliases     []string
	Description string
}

type slashPopupState struct {
	matches  []slashCommandSuggestion
	selected int
	visible  bool
}

func (p *slashPopupState) refresh(input string) {
	p.matches = filterSlashCommandSuggestions(input)
	p.selected = 0
	p.visible = len(p.matches) > 0
}

func (p *slashPopupState) move(delta int) {
	if !p.visible || len(p.matches) == 0 {
		return
	}
	p.selected = (p.selected + delta + len(p.matches)) % len(p.matches)
}

func (p *slashPopupState) accept() (string, bool) {
	if !p.visible || p.selected < 0 || p.selected >= len(p.matches) {
		return "", false
	}
	name := p.matches[p.selected].Name
	p.visible = false
	return name, true
}

// filterSlashCommandSuggestions 使用主命令和别名匹配输入，但每个命令只返回一次。
// filterSlashCommandSuggestions matches names and aliases while returning each
// canonical command at most once.
func filterSlashCommandSuggestions(input string) []slashCommandSuggestion {
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \t\r\n") {
		return nil
	}
	query := strings.ToLower(input)
	result := make([]slashCommandSuggestion, 0, len(slashCmds))
	for _, command := range slashCmds {
		matched := strings.HasPrefix(strings.ToLower(command.Name), query)
		if !matched {
			for _, alias := range command.Aliases {
				if strings.HasPrefix(strings.ToLower(alias), query) {
					matched = true
					break
				}
			}
		}
		if matched {
			result = append(result, slashCommandSuggestion{
				Name: command.Name, Aliases: append([]string(nil), command.Aliases...), Description: command.Description,
			})
		}
	}
	return result
}

func slashPopupWindow(total, selected, maxVisible int) (int, int) {
	if total <= 0 || maxVisible <= 0 {
		return 0, 0
	}
	if maxVisible > total {
		maxVisible = total
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	end := start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
	}
	return start, end
}

type slashPopupRow struct {
	Text     string
	Selected bool
}

func buildSlashPopupRows(popup slashPopupState, maxVisible, maxWidth int) ([]slashPopupRow, int, int) {
	if !popup.visible || len(popup.matches) == 0 || maxWidth < 6 {
		return nil, 0, 0
	}
	start, end := slashPopupWindow(len(popup.matches), popup.selected, maxVisible)
	rows := make([]slashPopupRow, 0, end-start)
	contentWidth := maxWidth - 4
	for index := start; index < end; index++ {
		item := popup.matches[index]
		aliasText := ""
		if len(item.Aliases) > 0 {
			aliasText = "  " + strings.Join(item.Aliases, " ")
		}
		text := fmt.Sprintf("%-14s %s%s", item.Name, item.Description, aliasText)
		rows = append(rows, slashPopupRow{Text: truncateDisplayWidth(text, contentWidth), Selected: index == popup.selected})
	}
	return rows, start, end
}

func truncateDisplayWidth(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if terminalDisplayWidth(value) <= limit {
		return value
	}
	var result strings.Builder
	used := 0
	for _, current := range value {
		width := runeDisplayWidth(current)
		if used+width+1 > limit {
			break
		}
		result.WriteRune(current)
		used += width
	}
	result.WriteRune('…')
	return result.String()
}

func terminalDisplayWidth(value string) int {
	width := 0
	for _, current := range value {
		width += runeDisplayWidth(current)
	}
	return width
}

func runeDisplayWidth(current rune) int {
	if current == 0 || current < 0x20 || (current >= 0x7f && current < 0xa0) {
		return 0
	}
	if unicode.Is(unicode.Mn, current) || unicode.Is(unicode.Me, current) {
		return 0
	}
	if current >= 0x1100 && (current <= 0x115f ||
		current == 0x2329 || current == 0x232a ||
		(current >= 0x2e80 && current <= 0xa4cf && current != 0x303f) ||
		(current >= 0xac00 && current <= 0xd7a3) ||
		(current >= 0xf900 && current <= 0xfaff) ||
		(current >= 0xfe10 && current <= 0xfe19) ||
		(current >= 0xfe30 && current <= 0xfe6f) ||
		(current >= 0xff00 && current <= 0xff60) ||
		(current >= 0xffe0 && current <= 0xffe6) ||
		(current >= 0x1f300 && current <= 0x1faff) ||
		(current >= 0x20000 && current <= 0x3fffd)) {
		return 2
	}
	return 1
}

func renderSlashPopup(out io.Writer, popup slashPopupState, maxVisible, maxWidth int) int {
	rows, start, end := buildSlashPopupRows(popup, maxVisible, maxWidth)
	if len(rows) == 0 {
		return 0
	}
	cyan := color.New(color.FgCyan, color.Bold)
	dim := color.New(color.FgHiBlack)
	green := color.New(color.FgGreen, color.Bold)

	fmt.Fprint(out, "\r\n\033[2K")
	cyan.Fprintf(out, "  命令 · %d 个匹配", len(popup.matches))
	for _, row := range rows {
		fmt.Fprint(out, "\r\n\033[2K")
		if row.Selected {
			green.Fprintf(out, "  › %s", row.Text)
		} else {
			dim.Fprintf(out, "    %s", row.Text)
		}
	}
	fmt.Fprint(out, "\r\n\033[2K")
	status := fmt.Sprintf("  ↑↓ 选择 · Tab/→ 补全 · Enter 执行 · %d-%d/%d", start+1, end, len(popup.matches))
	dim.Fprint(out, truncateDisplayWidth(status, maxWidth))
	lines := len(rows) + 2
	// 相对上移在 Windows ConPTY 中比 CSI s/u 保存恢复更可靠。
	// Relative movement is more reliable than CSI s/u save/restore on Windows ConPTY.
	fmt.Fprintf(out, "\033[%dA\r", lines)
	return lines
}

func clearSlashPopup(out io.Writer, lines int) {
	if lines <= 0 {
		return
	}
	for range lines {
		fmt.Fprint(out, "\r\n\033[2K")
	}
	fmt.Fprintf(out, "\033[%dA\r", lines)
}

// reserveSlashPopup 在首次展示前预留固定区域，先完成可能发生的滚屏。
// reserveSlashPopup reserves a fixed panel before relative redraws begin so any
// scrolling happens once, before the popup lifecycle starts.
func reserveSlashPopup(out io.Writer, lines int) {
	if lines <= 0 {
		return
	}
	for range lines {
		fmt.Fprint(out, "\r\n")
	}
	fmt.Fprintf(out, "\033[%dA\r", lines)
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

func printPromptRaw(s style.Style, provider string, mode config.WorkMode) {
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
	fmt.Printf("  %s \033[90m[%s mode:%s]\033[0m ", icon, provider, mode.Normalize())
}
