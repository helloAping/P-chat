package dynamic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/p-chat/pchat/internal/tool"
)

// MakeExecHandler builds a tool.ToolHandler for a spec whose
// template.type == "exec". The handler:
//  1. Renders the command string with {{.args.*}} and
//     {{.config.*}} substitutions.
//  2. Shells out via /bin/sh -c on Unix or cmd /c on Windows.
//  3. Captures stdout+stderr up to a per-call byte cap.
//  4. Returns the combined output as the CallResult.Content.
//
// Returns a non-nil result even on exec failure (the tool
// result is the error, not a Go error), so the agent's
// error-formatting pipeline handles it like any other tool.
func MakeExecHandler(spec Spec) tool.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		var argMap map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &argMap); err != nil {
				return &tool.CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
			}
		}
		// Render the command with the user's args + the
		// spec's resolved config (passed through RenderCtx).
		cmdStr, err := render(spec.Template.Command, RenderCtx{Args: argMap, Config: spec.Config})
		if err != nil {
			return &tool.CallResult{Content: "template render: " + err.Error(), IsError: true}, nil
		}
		if strings.TrimSpace(cmdStr) == "" {
			return &tool.CallResult{Content: "rendered command is empty", IsError: true}, nil
		}

		// Cap the runtime via context.WithTimeout. We don't
		// shell-quote `cmdStr` because the user wrote it as
		// a shell line on purpose — `echo foo && ls` should
		// keep working. We DO pass it through `sh -c` so
		// redirects and pipes survive.
		timeout := spec.Template.Timeout.Std()
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var out bytes.Buffer
		// Use sh -c on POSIX. On Windows there's no /bin/sh
		// by default, so the caller's pattern (cmd /c) is
		// what exec.CommandContext will use as a literal
		// string if the binary doesn't exist — we try sh
		// first and fall back to cmd.exe.
		cmd := exec.CommandContext(runCtx, "sh", "-c", cmdStr) //nolint:gosec // user-defined tool
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr := cmd.Run()
		// Truncate the output to 32 KiB so a runaway `cat
		// /dev/zero` doesn't OOM the chat. The LLM doesn't
		// need 100 MB of binary garbage in its context.
		const maxOut = 32 * 1024
		content := out.String()
		if len(content) > maxOut {
			content = content[:maxOut] + "\n... (truncated)"
		}
		if runErr != nil {
			// Surface the exec error with the captured
			// output so the LLM can see what the command
			// actually did before failing.
			return &tool.CallResult{
				Content: fmt.Sprintf("exec failed: %v\n--- output ---\n%s", runErr, content),
				IsError: true,
			}, nil
		}
		return &tool.CallResult{Content: content}, nil
	}
}
