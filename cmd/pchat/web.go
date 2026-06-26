// Command `pchat web` is the shortest path to "open P-Chat's Web UI
// on this machine". It does four things:
//
//  1. Locates the pchat-server binary (sibling of pchat by default,
//     override with --bin).
//  2. Forks it via serverproc.Start and waits for /api/v1/health.
//  3. Opens the default browser to /app/index.html (skip with
//     --no-open).
//  4. Blocks until Ctrl+C / SIGTERM, then cleanly stops the child
//     via serverproc.Stop (which taskkills on Windows) before
//     returning.
//
// The previous version of this command missed step 4: cobra's
// default handler calls os.Exit(0) on SIGINT, which does NOT run
// deferred cleanups, so pchat-server kept running as an orphan.
// The fix is a small signal.Notify loop that calls Stop explicitly.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/p-chat/pchat/internal/serverproc"
)

var (
	webPort    int
	webNoOpen  bool
	webBin     string
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "启动 pchat-server 并在浏览器中打开 Web UI",
	Long: `在"一台机器上打开 Web UI"这件事的最短路径：

  pchat web

默认行为:
  1. 在同目录自动查找 pchat-server.exe（兄弟文件）
  2. serverproc.Start fork 它，等待 /api/v1/health 返回 200
  3. 在默认浏览器里打开 /app/index.html
  4. 阻塞直到 Ctrl+C，然后干净地关掉子进程`,
	RunE: runWeb,
}

func init() {
	webCmd.Flags().IntVar(&webPort, "port", 0, "pchat-server 监听端口 (0=自动)")
	webCmd.Flags().BoolVar(&webNoOpen, "no-open", false, "不打开浏览器")
	webCmd.Flags().StringVar(&webBin, "bin", "", "pchat-server 二进制路径(默认: 与 pchat 同目录)")
}

func runWeb(cmd *cobra.Command, args []string) error {
	bin, err := resolveServerBinary(webBin)
	if err != nil {
		return err
	}

	// Resolve the absolute path to the web/ static dir. pchat-server
	// needs to serve web/index.html from /app/ when the user opens
	// the URL in a browser. Without this, launching `pchat web`
	// from a non-repo CWD (e.g. bin/) makes /app return 404 because
	// pchat-server looks for `web/` relative to its own CWD.
	//
	// Strategy: prefer the web/ folder next to pchat.exe (the usual
	// install layout — web/ is a sibling of bin/). Fall back to the
	// current working directory's web/ (works when running from
	// the repo root during development).
	webDir := resolveWebDir()

	cyan := color.New(color.FgCyan, color.Bold)
	green := color.New(color.FgGreen, color.Bold)
	dim := color.New(color.FgHiBlack)
	dim.Printf("  • pchat-server: %s\n", bin)
	dim.Printf("  • 静态资源:   %s\n", webDir)
	dim.Printf("  • 端口: ")
	if webPort == 0 {
		dim.Println("自动挑选空闲端口")
	} else {
		dim.Printf("%d\n", webPort)
	}

	// Install the signal handler BEFORE Start so a Ctrl+C during
	// the (up to 15s) startup phase is also caught instead of being
	// delivered to Go's default handler, which would TerminateProcess
	// and leave pchat-server as an orphan.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Start the child. serverproc.Start blocks until /health is
	// 200 OK, so a failed boot surfaces here (e.g. bad config).
	srv, err := serverproc.Start(ctx, serverproc.Options{
		ServerBin:   bin,
		Port:        webPort,
		ConfigPath:  cfgFile,
		WebDir:      webDir,
		PingTimeout: 15 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("启动 pchat-server 失败: %w", err)
	}

	// Safety net: even if the signal handler path somehow misses a
	// signal, a deferred Stop on return means any error path (panic
	// in our code, unexpected return) still cleans up the child.
	defer srv.Stop()

	url := fmt.Sprintf("%s/app/index.html", srv.BaseURL)

	fmt.Println()
	green.Println("  P-Chat Web 已就绪")
	cyan.Printf("    %s\n", url)
	fmt.Println()
	dim.Println("  Ctrl+C 退出并停止 pchat-server")
	fmt.Println()

	if !webNoOpen {
		// Best-effort: the browser launch is fire-and-forget.
		// We never propagate its exit code or its errors — the
		// user can always paste the URL in manually.
		if err := openBrowser(url); err != nil {
			dim.Printf("  (无法自动打开浏览器: %v)\n", err)
		}
	}

	// Block until SIGINT. We deliberately do NOT start a
	// goroutine: when sigCh fires, we run Stop() inline so the
	// child's exit status is logged before we return.
	sig := <-sigCh
	dim.Printf("\n  收到信号 %v, 正在停止 pchat-server...\n", sig)
	srv.Stop()
	dim.Println("  done.")

	// Bypass any deferred work from cobra / gin (cobra waits on
	// its own things, and we want the child to have been killed
	// before this returns). os.Exit(0) does NOT run deferred
	// funcs, so the order matters: we did srv.Stop() above, then
	// call Exit. This guarantees a clean shutdown.
	os.Exit(0)
	return nil // unreachable
}

// resolveWebDir finds the absolute path to the web/ static dir.
//
// Order:
//  1. <dir-of-pchat.exe>/../web            (installed layout: bin/ + ../web)
//  2. <cwd>/web                            (dev layout: running from repo root)
//  3. "" — caller falls back to CWD-relative (server default)
//
// The returned path is the FIRST one that exists.
func resolveWebDir() string {
	candidates := make([]string, 0, 3)
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		// sibling-of-exe: bin/web (when installed as bin/pchat.exe + bin/web/)
		candidates = append(candidates, filepath.Join(exeDir, "web"))
		// one-up: <install>/web (when installed as install/bin/pchat.exe + install/web/)
		candidates = append(candidates, filepath.Join(exeDir, "..", "web"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "web"))
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			return abs
		}
	}
	return ""
}

// openBrowser asks the OS to open url in the default browser. The
// launched process is intentionally NOT waited on: it's a detached
// helper, and the user pressing Ctrl+C in pchat should not close
// the browser window. SetSysProcAttrNewPG puts the child in its
// own process group on Unix; on Windows Go's exec.Cmd already
// detaches non-Wait'd children.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 url.dll,FileProtocolHandler <url>  — the
		// canonical Windows shell-open.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // linux, *bsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	serverproc.SetSysProcAttrNewPG(cmd)
	return cmd.Start()
}
