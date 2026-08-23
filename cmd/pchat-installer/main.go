package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
)

//go:embed assets
var bundled embed.FS

func main() {
	tmp, err := os.MkdirTemp("", "pchat-setup")
	if err != nil {
		fail("创建临时目录失败", err)
	}

	if err := extractAssets(tmp); err != nil {
		fail("解压文件失败", err)
	}

	if err := runInstall(tmp, os.Args[1:]); err != nil {
		fail(fmt.Sprintf("安装失败，临时文件保留在: %s", tmp), err)
	}

	if err := os.RemoveAll(tmp); err != nil {
		showInstallerError("P-Chat 安装程序", fmt.Sprintf("安装已完成，但清理临时目录失败，可手动删除:\n%s\n\n%v", tmp, err))
	}
}

func extractAssets(dest string) error {
	entries, err := bundled.ReadDir("assets")
	if err != nil {
		return err
	}
	for _, e := range entries {
		src := path.Join("assets", e.Name())
		if e.IsDir() {
			if err := copyDir(src, filepath.Join(dest, e.Name())); err != nil {
				return fmt.Errorf("复制 %s: %w", e.Name(), err)
			}
		} else {
			dst := filepath.Join(dest, e.Name())
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("复制 %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

func copyFile(src string, dst string) error {
	data, err := bundled.ReadFile(src)
	if err != nil {
		return err
	}
	if filepath.Ext(dst) == ".ps1" && !hasUTF8BOM(data) {
		data = append([]byte{0xEF, 0xBB, 0xBF}, data...)
	}
	return os.WriteFile(dst, data, 0644)
}

func hasUTF8BOM(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF
}

func copyDir(src string, dst string) error {
	entries, err := bundled.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, e := range entries {
		s := path.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func runInstall(tmp string, args []string) error {
	ps1 := filepath.Join(tmp, "install.ps1")
	psArgs := installPowerShellArgs(ps1, args)
	cmd := exec.Command("powershell", psArgs...)
	cmd.Dir = tmp
	configureInstallCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("%w\n\n%s", err, string(output))
		}
		return err
	}
	return nil
}

func installPowerShellArgs(ps1 string, args []string) []string {
	psArgs := []string{"-STA", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1}
	if len(args) == 0 {
		psArgs = append(psArgs, "-Gui")
	} else {
		psArgs = append(psArgs, args...)
	}
	return psArgs
}

func fail(msg string, err error) {
	text := msg
	if err != nil {
		if text != "" {
			text += "\n\n"
		}
		text += err.Error()
	}
	showInstallerError("P-Chat 安装程序", text)
	os.Exit(1)
}
