// Command pchat-updater applies a downloaded P-Chat update zip after the
// running GUI has exited.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/p-chat/pchat/internal/version"
)

type options struct {
	installDir     string
	packagePath    string
	launch         string
	logPath        string
	expectedSHA256 string
	parentPID      int
	waitTimeout    time.Duration
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("P-Chat Updater " + version.FullString())
		return
	}
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	logger, closeLog := newLogger(opts.logPath, opts.packagePath)
	defer closeLog()

	if err := run(opts, logger); err != nil {
		logger.Printf("update failed: %v", err)
		os.Exit(1)
	}
	logger.Print("update completed")
}

func parseFlags(args []string) (options, error) {
	fs := flag.NewFlagSet("pchat-updater", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts options
	timeout := fs.String("wait-timeout", "60s", "maximum time to wait for running files to be released")
	fs.StringVar(&opts.installDir, "install-dir", "", "P-Chat install directory")
	fs.StringVar(&opts.packagePath, "package", "", "downloaded update zip")
	fs.StringVar(&opts.launch, "launch", "", "binary to launch after update")
	fs.StringVar(&opts.logPath, "log", "", "update log path")
	fs.StringVar(&opts.expectedSHA256, "expected-sha256", "", "expected update zip SHA-256")
	fs.IntVar(&opts.parentPID, "parent-pid", 0, "parent GUI process id")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if strings.TrimSpace(opts.installDir) == "" {
		return options{}, errors.New("--install-dir is required")
	}
	if strings.TrimSpace(opts.packagePath) == "" {
		return options{}, errors.New("--package is required")
	}
	d, err := time.ParseDuration(*timeout)
	if err != nil {
		return options{}, fmt.Errorf("parse --wait-timeout: %w", err)
	}
	opts.waitTimeout = d
	return opts, nil
}

func newLogger(logPath string, packagePath string) (*log.Logger, func()) {
	if strings.TrimSpace(logPath) == "" {
		base := filepath.Dir(packagePath)
		if base == "." || base == "" {
			base = os.TempDir()
		}
		logPath = filepath.Join(base, "update.log")
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return log.New(io.Discard, "", 0), func() {}
	}
	logger := log.New(f, "[pchat-updater] ", log.LstdFlags|log.Lmicroseconds)
	return logger, func() { _ = f.Close() }
}

func run(opts options, logger *log.Logger) error {
	installDir, err := filepath.Abs(opts.installDir)
	if err != nil {
		return fmt.Errorf("resolve install dir: %w", err)
	}
	packagePath, err := filepath.Abs(opts.packagePath)
	if err != nil {
		return fmt.Errorf("resolve update package: %w", err)
	}
	if !strings.EqualFold(filepath.Ext(packagePath), ".zip") {
		return fmt.Errorf("update package must be a zip file: %s", packagePath)
	}
	if err := verifyPackageHash(packagePath, opts.expectedSHA256); err != nil {
		return err
	}
	if opts.parentPID > 0 {
		waitForProcessExit(opts.parentPID, opts.waitTimeout, logger)
	}
	logger.Printf("applying package=%s install_dir=%s", packagePath, installDir)
	backupDir, err := applyUpdateZip(packagePath, installDir, opts.waitTimeout, logger)
	if err != nil {
		return err
	}
	logger.Printf("backup_dir=%s", backupDir)
	if strings.TrimSpace(opts.launch) != "" {
		if err := launchUpdatedApp(installDir, opts.launch, logger); err != nil {
			return err
		}
	}
	return nil
}

func verifyPackageHash(packagePath string, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return nil
	}
	f, err := os.Open(packagePath)
	if err != nil {
		return fmt.Errorf("open update package: %w", err)
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("hash update package: %w", err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != expected {
		return fmt.Errorf("update package sha256 mismatch: got %s, want %s", got, expected)
	}
	return nil
}

func applyUpdateZip(packagePath string, installDir string, waitTimeout time.Duration, logger *log.Logger) (string, error) {
	reader, err := zip.OpenReader(packagePath)
	if err != nil {
		return "", fmt.Errorf("open update zip: %w", err)
	}
	defer reader.Close()

	stage, err := os.MkdirTemp(filepath.Dir(packagePath), "pchat-update-stage-*")
	if err != nil {
		return "", fmt.Errorf("create update stage: %w", err)
	}
	defer os.RemoveAll(stage)

	var files []string
	for _, zf := range reader.File {
		rel, err := cleanZipPath(zf.Name)
		if err != nil {
			return "", err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		dst, err := targetPath(stage, rel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", fmt.Errorf("create stage dir: %w", err)
		}
		if err := extractZipFile(zf, dst); err != nil {
			return "", err
		}
		files = append(files, rel)
	}
	if len(files) == 0 {
		return "", errors.New("update zip contains no files")
	}

	backupDir := filepath.Join(filepath.Dir(packagePath), "backups", time.Now().Format("20060102-150405"))
	var replaced []string
	for _, rel := range files {
		src, err := targetPath(stage, rel)
		if err != nil {
			return "", err
		}
		dst, err := targetPath(installDir, rel)
		if err != nil {
			return "", err
		}
		if err := backupExistingFile(dst, backupDir, rel); err != nil {
			return "", err
		}
		info, err := os.Stat(src)
		if err != nil {
			return "", fmt.Errorf("stat staged file: %w", err)
		}
		mode := updateFileMode(rel, info.Mode())
		replaced = append(replaced, rel)
		if err := replaceFileWithRetry(src, dst, mode, waitTimeout); err != nil {
			rollbackFiles(backupDir, installDir, replaced, logger)
			return "", err
		}
	}
	return backupDir, nil
}

func extractZipFile(zf *zip.File, dst string) error {
	rc, err := zf.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", zf.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode())
	if err != nil {
		return fmt.Errorf("create staged file %s: %w", dst, err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		_ = out.Close()
		return fmt.Errorf("extract zip entry %s: %w", zf.Name, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close staged file %s: %w", dst, err)
	}
	return nil
}

func backupExistingFile(dst string, backupDir string, rel string) error {
	info, err := os.Stat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat installed file %s: %w", dst, err)
	}
	if info.IsDir() {
		return fmt.Errorf("installed path is a directory: %s", dst)
	}
	backupPath, err := targetPath(backupDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	return copyFile(dst, backupPath, info.Mode())
}

func updateFileMode(rel string, mode os.FileMode) os.FileMode {
	return updateFileModeForOS(rel, mode, runtime.GOOS)
}

func updateFileModeForOS(rel string, mode os.FileMode, goos string) os.FileMode {
	if goos == "windows" {
		return mode
	}
	cleaned := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	base := path.Base(cleaned)
	switch base {
	case "pchat", "pchat-gui", "pchat-server", "pchat-updater", "install.sh", "uninstall.sh":
		return executableMode(mode)
	}
	if goos == "darwin" &&
		(strings.HasPrefix(cleaned, "Contents/MacOS/") ||
			strings.HasPrefix(cleaned, "Contents/Resources/pchat")) {
		return executableMode(mode)
	}
	return mode
}

func executableMode(mode os.FileMode) os.FileMode {
	return (mode &^ os.ModePerm) | 0o755
}

func replaceFileWithRetry(src string, dst string, mode os.FileMode, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		err := replaceFileOnce(src, dst, mode)
		if err == nil {
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("replace %s: %w", dst, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func replaceFileOnce(src string, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pchat-new-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)

	if err := copyFile(src, tmpPath, mode); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func copyFile(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func rollbackFiles(backupDir string, installDir string, replaced []string, logger *log.Logger) {
	for i := len(replaced) - 1; i >= 0; i-- {
		rel := replaced[i]
		src, err := targetPath(backupDir, rel)
		if err != nil {
			continue
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst, err := targetPath(installDir, rel)
		if err != nil {
			continue
		}
		info, err := os.Stat(src)
		if err != nil {
			continue
		}
		if err := copyFile(src, dst, info.Mode()); err != nil {
			logger.Printf("rollback %s failed: %v", rel, err)
		}
	}
}

func cleanZipPath(name string) (string, error) {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if name == "" {
		return "", errors.New("zip entry has empty name")
	}
	if path.IsAbs(name) || hasWindowsVolume(name) {
		return "", fmt.Errorf("unsafe absolute zip entry: %s", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe zip entry path: %s", name)
	}
	return filepath.FromSlash(cleaned), nil
}

func hasWindowsVolume(name string) bool {
	if len(name) >= 2 && name[1] == ':' {
		c := name[0]
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	return false
}

func targetPath(root string, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(rootAbs, rel)
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return "", err
	}
	back, err := filepath.Rel(rootAbs, dstAbs)
	if err != nil {
		return "", err
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) || filepath.IsAbs(back) {
		return "", fmt.Errorf("target escapes root: %s", rel)
	}
	return dstAbs, nil
}

func waitForProcessExit(pid int, timeout time.Duration, logger *log.Logger) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			logger.Printf("parent process %d exited", pid)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	logger.Printf("parent process %d still visible after %s; continuing with file retry loop", pid, timeout)
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return true
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func launchUpdatedApp(installDir string, launch string, logger *log.Logger) error {
	target := launch
	if !filepath.IsAbs(target) {
		target = filepath.Join(installDir, launch)
	}
	cmd := exec.Command(target)
	cmd.Dir = installDir
	hideChildConsole(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch updated app: %w", err)
	}
	logger.Printf("launched %s pid=%d", target, cmd.Process.Pid)
	return nil
}
