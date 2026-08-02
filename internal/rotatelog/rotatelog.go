// Package rotatelog 提供按日期切割的文件日志 writer：每个自然日
// 一个文件，写入前自动滚动到当天文件，并清理超过保留期（默认 7 天）
// 的旧日志文件。
package rotatelog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Writer 是线程安全的按日期切割日志 writer，实现 io.Writer。
// 文件名格式：<base>-YYYY-MM-DD.log（保留名不携带日期，仅在删除时精确匹配）。
type Writer struct {
	mu   sync.Mutex
	dir  string
	base string // 文件基名，如 "server-debug"
	ret  int    // 保留天数
	open bool
	cur  string   // 当前打开文件的完整路径
	day  string   // 当前文件对应的日期 YYYY-MM-DD
	f    *os.File
}

// ErrOpenFailed 表示日志目录创建或文件打开失败（返回给调用方以便降级）。
var ErrOpenFailed = errors.New("rotatelog: open log file failed")

// New 返回一个写入 dir 目录、文件名为 <base>-<date>.log 的 writer。
// dir 不存在时会尝试创建；创建失败或打开今日文件失败返回 ErrOpenFailed。
// retentionDays 为保留天数（<=0 时使用默认 7 天）。
func New(dir, base string, retentionDays int) (*Writer, error) {
	if retentionDays <= 0 {
		retentionDays = 7
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: mkdir %s: %v", ErrOpenFailed, dir, err)
	}
	w := &Writer{dir: dir, base: base, ret: retentionDays}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Close 关闭当前日志文件。之后 Write 会因文件未打开而返回错误。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.open {
		return nil
	}
	err := w.f.Close()
	w.open = false
	return err
}

// Write 追加数据到当日日志文件。若日期已切换则先滚动到新文件，
// 再清理超过保留期的旧文件。任何失败都返回错误，由调用方决定是否
// 降级输出。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotate(); err != nil {
		return 0, err
	}
	return w.f.Write(p)
}

// rotate 确保 f 指向今天的文件；跨天时关闭旧文件、打开新文件并
// 触发一次过期文件清理。
func (w *Writer) rotate() error {
	day := time.Now().Format("2006-01-02")
	if w.open && day == w.day {
		return nil
	}
	if w.open {
		_ = w.f.Close()
		w.open = false
	}
	name := fmt.Sprintf("%s-%s.log", w.base, day)
	f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOpenFailed, err)
	}
	w.f = f
	w.cur = filepath.Join(w.dir, name)
	w.day = day
	w.open = true
	w.cleanup()
	return nil
}

// cleanup 删除 dir 下所有匹配 <base>-<date>.log 且日期早于保留期的文件。
// 只精确匹配本 writer 的基名，不会误删其它日志。
func (w *Writer) cleanup() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	prefix := w.base + "-"
	cutoff := time.Now().AddDate(0, 0, -w.ret)
	type cand struct {
		path string
		mod  time.Time
	}
	var olds []cand
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// 仅按名称解析日期，避免依赖文件 mtime（滚动时被覆盖）。
		datePart := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		t, err := time.ParseInLocation("2006-01-02", datePart, time.Local)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			olds = append(olds, cand{path: filepath.Join(w.dir, name), mod: info.ModTime()})
		}
	}
	sort.Slice(olds, func(i, j int) bool { return olds[i].mod.Before(olds[j].mod) })
	for _, o := range olds {
		_ = os.Remove(o.path)
	}
}
