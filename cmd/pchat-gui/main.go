// Command pchat-gui launches the Windows desktop UI for P-Chat.
//
// The current implementation is a thin CLI wrapper that prints a
// message: the actual Wails v2 frontend is still TODO. See
// internal/todo for the design notes and the cmd/pchat-gui/wails
// subdirectory layout that will be created when the Go build target
// is set up.
//
// To enable the UI:
//   1. Install Wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest
//   2. Install Node 18+
//   3. From the repo root: wails init -n pchat-gui -t vanilla
//   4. Move the generated frontend into cmd/pchat-gui/frontend/
//   5. Replace this stub with a real Wails app (see TODO below).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	cmd := &cobra.Command{
		Use:   "pchat-gui",
		Short: "Launch the P-Chat Windows desktop UI (Wails v2)",
		Long: `pchat-gui is the Windows-native frontend for P-Chat.

Status: stub. The Wails v2 frontend is not yet built; this binary
just prints a reminder and exits. Until the GUI is implemented, use
"pchat" (the terminal REPL) or "pchat-server" (the HTTP API).

Planned tech stack:
  - Wails v2 (Go + webview, single binary, ~10MB)
  - Vanilla TypeScript + Vite (no React/Vue framework lock-in)
  - Terminal-style chat UI: monospace, dark theme, command palette
  - All existing REPL commands available via keyboard shortcuts
`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("P-Chat GUI is not yet built. The Wails v2 frontend is on the roadmap.")
			fmt.Println("In the meantime, run `pchat` for the terminal REPL or `pchat-server` for the HTTP API.")
			os.Exit(0)
		},
	}
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
