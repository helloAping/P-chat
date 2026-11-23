package cli

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFilterSlashCommandSuggestionsShowsAllCommands(t *testing.T) {
	got := filterSlashCommandSuggestions("/")
	if len(got) != len(slashCmds) {
		t.Fatalf("filter(/) returned %d commands, want %d", len(got), len(slashCmds))
	}
	seen := make(map[string]bool, len(got))
	for _, suggestion := range got {
		if seen[suggestion.Name] {
			t.Fatalf("filter(/) returned duplicate command %q", suggestion.Name)
		}
		seen[suggestion.Name] = true
	}
}

func TestFilterSlashCommandSuggestionsFiltersNamesAndAliases(t *testing.T) {
	got := filterSlashCommandSuggestions("/mo")
	if len(got) != 2 || got[0].Name != "/mode" || got[1].Name != "/model" {
		t.Fatalf("filter(/mo) = %+v, want /mode and /model", got)
	}

	alias := filterSlashCommandSuggestions("/?")
	if len(alias) != 1 || alias[0].Name != "/help" {
		t.Fatalf("filter(/?) = %+v, want canonical /help", alias)
	}

	if got := filterSlashCommandSuggestions("/model "); len(got) != 0 {
		t.Fatalf("filter with command arguments = %+v, want hidden popup", got)
	}
	if got := filterSlashCommandSuggestions("hello"); len(got) != 0 {
		t.Fatalf("filter(non-command) = %+v, want no suggestions", got)
	}
}

func TestSlashPopupStateMovesWrapsAndAcceptsCanonicalCommand(t *testing.T) {
	var popup slashPopupState
	popup.refresh("/mo")
	if !popup.visible || popup.selected != 0 {
		t.Fatalf("initial popup = %+v, want visible selection 0", popup)
	}

	popup.move(-1)
	if popup.selected != 1 {
		t.Fatalf("move up wrapped to %d, want 1", popup.selected)
	}
	command, ok := popup.accept()
	if !ok || command != "/model" || popup.visible {
		t.Fatalf("accept = %q, %v, visible=%v; want /model, true, false", command, ok, popup.visible)
	}
}

func TestSlashPopupWindowFollowsSelection(t *testing.T) {
	start, end := slashPopupWindow(20, 0, 8)
	if start != 0 || end != 8 {
		t.Fatalf("window at start = %d:%d, want 0:8", start, end)
	}
	start, end = slashPopupWindow(20, 11, 8)
	if start != 4 || end != 12 {
		t.Fatalf("window at selection 11 = %d:%d, want 4:12", start, end)
	}
}

func TestSlashPopupTerminalSizePrefersStdoutAndLeavesWrapColumn(t *testing.T) {
	const (
		stdinFD  = 10
		stdoutFD = 11
	)
	width, height := slashPopupTerminalSize(stdinFD, stdoutFD, func(fd int) (int, int, error) {
		switch fd {
		case stdoutFD:
			return 60, 24, nil
		case stdinFD:
			return 120, 40, nil
		default:
			return 0, 0, fmt.Errorf("unexpected fd %d", fd)
		}
	})
	if width != 59 || height != 24 {
		t.Fatalf("popup terminal size = %dx%d, want 59x24", width, height)
	}
}

func TestSlashPopupTerminalSizeFallsBackToStdin(t *testing.T) {
	const (
		stdinFD  = 10
		stdoutFD = 11
	)
	width, height := slashPopupTerminalSize(stdinFD, stdoutFD, func(fd int) (int, int, error) {
		if fd == stdoutFD {
			return 0, 0, fmt.Errorf("stdout size unavailable")
		}
		return 90, 30, nil
	})
	if width != 89 || height != 30 {
		t.Fatalf("fallback popup terminal size = %dx%d, want 89x30", width, height)
	}
}

func TestBuildAndRenderSlashPopup(t *testing.T) {
	var popup slashPopupState
	popup.refresh("/")
	popup.selected = 9
	rows, start, end := buildSlashPopupRows(popup, 4, 60)
	if len(rows) != 4 || start != 6 || end != 10 || !rows[3].Selected {
		t.Fatalf("rows=%+v window=%d:%d, want four rows ending at selected item", rows, start, end)
	}

	var output bytes.Buffer
	lines := renderSlashPopup(&output, popup, 4, 60)
	if lines != 6 {
		t.Fatalf("rendered lines = %d, want 6", lines)
	}
	text := output.String()
	if !strings.Contains(text, "命令") || !strings.Contains(text, "Enter 执行") {
		t.Fatalf("popup output missing header or controls: %q", text)
	}
	for _, row := range rows {
		if width := terminalDisplayWidth("  › " + row.Text); width > 60 {
			t.Fatalf("popup row width = %d, want <= 60: %q", width, row.Text)
		}
	}
}

func TestTruncateDisplayWidthPreservesUTF8(t *testing.T) {
	if got := truncateDisplayWidth("命令候选abc", 5); got != "命令…" {
		t.Fatalf("truncateDisplayWidth = %q, want %q", got, "命令…")
	}
}

func TestReserveSlashPopupCreatesSpaceBeforeRelativeRedraw(t *testing.T) {
	var output bytes.Buffer
	reserveSlashPopup(&output, 3)
	if got := output.String(); got != "\r\n\r\n\r\n\033[3A\r" {
		t.Fatalf("reserveSlashPopup output = %q", got)
	}
}

func TestSlashPopupRedrawDoesNotLeaveDuplicatePanelsWhenCursorSaveUnsupported(t *testing.T) {
	var popup slashPopupState
	popup.refresh("/")

	const (
		maxVisible    = 4
		terminalWidth = 60
		prompt        = "PROMPT /"
	)
	popupWidth, _ := slashPopupTerminalSize(10, 11, func(fd int) (int, int, error) {
		if fd == 11 {
			return terminalWidth, 24, nil
		}
		return 120, 40, nil
	})
	var output bytes.Buffer
	fmt.Fprint(&output, prompt)
	reserveSlashPopup(&output, maxVisible+2)
	fmt.Fprint(&output, "\r\033[2K", prompt)
	popupLines := renderSlashPopup(&output, popup, maxVisible, popupWidth)

	popup.move(1)
	clearSlashPopup(&output, popupLines)
	fmt.Fprint(&output, "\r\033[2K", prompt)
	renderSlashPopup(&output, popup, maxVisible, popupWidth)

	screen := emulateANSIScreenWithoutSavedCursor(output.String(), terminalWidth)
	if count := strings.Count(screen, "命令 ·"); count != 1 {
		t.Fatalf("redraw left %d command panels, want 1:\n%s", count, screen)
	}
	if count := strings.Count(screen, prompt); count != 1 {
		t.Fatalf("redraw left %d prompts, want 1:\n%s", count, screen)
	}
}

// emulateANSIScreenWithoutSavedCursor 模拟支持相对移动但忽略 CSI s/u 的终端。
// emulateANSIScreenWithoutSavedCursor models a terminal that supports relative
// movement but ignores CSI s/u cursor save and restore sequences.
func emulateANSIScreenWithoutSavedCursor(output string, width int) string {
	lines := [][]rune{{}}
	row, column := 0, 0
	ensureRow := func(target int) {
		for len(lines) <= target {
			lines = append(lines, nil)
		}
	}
	writeRune := func(value rune) {
		cellWidth := runeDisplayWidth(value)
		if width > 0 && column+cellWidth > width {
			row++
			column = 0
		}
		ensureRow(row)
		for len(lines[row]) < column+cellWidth {
			lines[row] = append(lines[row], ' ')
		}
		lines[row][column] = value
		for offset := 1; offset < cellWidth; offset++ {
			lines[row][column+offset] = 0
		}
		column += cellWidth
	}

	for index := 0; index < len(output); {
		switch output[index] {
		case '\r':
			column = 0
			index++
			continue
		case '\n':
			row++
			ensureRow(row)
			index++
			continue
		case 0x1b:
			if index+1 < len(output) && output[index+1] == '[' {
				end := index + 2
				for end < len(output) && (output[end] < 0x40 || output[end] > 0x7e) {
					end++
				}
				if end < len(output) {
					parameter := output[index+2 : end]
					switch output[end] {
					case 'A':
						amount := 1
						if parameter != "" {
							if parsed, err := strconv.Atoi(strings.Split(parameter, ";")[0]); err == nil {
								amount = parsed
							}
						}
						row -= amount
						if row < 0 {
							row = 0
						}
					case 'K':
						if parameter == "2" || parameter == "" {
							ensureRow(row)
							lines[row] = nil
						}
					case 's', 'u', 'm':
						// Ignore saved cursor and styling sequences in this compatibility model.
					}
					index = end + 1
					continue
				}
			}
		}
		value, size := utf8.DecodeRuneInString(output[index:])
		writeRune(value)
		index += size
	}

	textLines := make([]string, len(lines))
	for index, line := range lines {
		var text strings.Builder
		for _, value := range line {
			if value != 0 {
				text.WriteRune(value)
			}
		}
		textLines[index] = strings.TrimRight(text.String(), " ")
	}
	return strings.Join(textLines, "\n")
}
