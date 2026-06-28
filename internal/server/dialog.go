package server

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// nativePickFolder opens the OS-native folder selection dialog
// and returns the chosen absolute path, or "" if cancelled.
func nativePickFolder() (string, error) {
	switch runtime.GOOS {
	case "windows":
		return pickFolderWindows()
	default:
		return "", fmt.Errorf("folder picker not supported on %s", runtime.GOOS)
	}
}

func pickFolderWindows() (string, error) {
	// Use PowerShell to open the native .NET FolderBrowserDialog.
	// The script writes the selected path to stdout; errors go to stderr.
	script := `
Add-Type -AssemblyName System.Windows.Forms
$dlg = New-Object System.Windows.Forms.FolderBrowserDialog
$dlg.Description = "选择项目目录"
$dlg.ShowNewFolderButton = $true
if ($dlg.ShowDialog() -eq 'OK') {
    Write-Output $dlg.SelectedPath
}
`
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("folder picker failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
