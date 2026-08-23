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
	visualMode := len(os.Args) == 1
	tmp, err := os.MkdirTemp("", "pchat-setup")
	if err != nil {
		fail("创建临时目录失败", err)
	}

	fmt.Println("P-Chat 安装程序")
	fmt.Println("────────────────")
	fmt.Println()

	if err := extractAssets(tmp); err != nil {
		fail("解压文件失败", err)
	}

	if err := runInstall(tmp, os.Args[1:]); err != nil {
		fmt.Println()
		fmt.Println("安装失败，临时文件保留在:", tmp)
		fail("", err)
	}

	if err := os.RemoveAll(tmp); err != nil {
		fmt.Println("(清理临时文件失败, 可手动删除:", tmp, ")")
	}

	fmt.Println()
	fmt.Println("安装程序已结束。")
	if !visualMode {
		pause()
	}
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

func runInstall(tmp string, args []string) error {
	fmt.Print("执行安装脚本... ")
	ps1 := filepath.Join(tmp, "install.ps1")
	psArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1}
	if len(args) == 0 {
		psArgs = append(psArgs, "-Gui")
	} else {
		psArgs = append(psArgs, args...)
	}
	cmd := exec.Command("powershell", psArgs...)
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
