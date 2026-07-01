package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

//go:embed assets
var bundled embed.FS

func main() {
	tmp, err := os.MkdirTemp("", "pchat-setup")
	if err != nil {
		fail("创建临时目录失败", err)
	}

	fmt.Println("P-Chat 安装程序")
	fmt.Println("────────────────")
	fmt.Println()

	installDir := pickDir()
	if installDir == "" {
		fmt.Println("已取消。")
		pause()
		return
	}

	if err := extractAssets(tmp); err != nil {
		fail("解压文件失败", err)
	}

	if err := runInstall(tmp, installDir); err != nil {
		fmt.Println()
		fmt.Println("安装失败，临时文件保留在:", tmp)
		fail("", err)
	}

	if err := os.RemoveAll(tmp); err != nil {
		fmt.Println("(清理临时文件失败, 可手动删除:", tmp, ")")
	}

	fmt.Println()
	fmt.Println("安装完成! 可从开始菜单或桌面快捷方式启动 P-Chat。")
	pause()
}

func extractAssets(dest string) error {
	fmt.Print("解压文件... ")
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
	fmt.Println("完成")
	return nil
}

func copyFile(src string, dst string) error {
	data, err := bundled.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
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

func pickDir() string {
	fmt.Println("请选择安装目录…")

	path := pickDirGUI()
	if path == "" {
		fmt.Print("请输入安装目录路径: ")
		fmt.Scanln(&path)
		path = strings.TrimSpace(path)
	}
	if path == "" {
		return ""
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Printf("无效路径: %v\n", err)
		return ""
	}

	if err := os.MkdirAll(abs, 0755); err != nil {
		fmt.Printf("无法创建目录: %v\n", err)
		return ""
	}

	fmt.Printf("安装目录: %s\n\n", abs)
	return abs
}

func pickDirGUI() string {
	ps := `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = "选择 P-Chat 安装目录"
$d.ShowNewFolderButton = $true
if ($d.ShowDialog() -eq 'OK') { $d.SelectedPath }
`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runInstall(tmp, target string) error {
	fmt.Print("执行安装脚本... ")
	ps1 := filepath.Join(tmp, "install.ps1")
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", ps1,
		"-InstallDir", target,
		"-AddToPath",
	)
	cmd.Dir = tmp
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func pause() {
	fmt.Println()
	fmt.Print("按 Enter 退出...")
	fmt.Scanln()
}

func fail(msg string, err error) {
	if msg != "" {
		fmt.Fprintf(os.Stderr, "错误: %s\n", msg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
	}
	pause()
	os.Exit(1)
}
