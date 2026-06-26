package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/knowledge"
)

// KBManager manages the user's attached knowledge bases. A knowledge
// base is just a directory of .md / .txt files; the manager keeps a
// list of attached paths in a small YAML file under ~/.p-chat/ and
// exposes commands to add/remove/scan them.
type KBManager struct {
	indexer *knowledge.Indexer
	paths   []string
}

func NewKBManager(indexer *knowledge.Indexer, paths []string) *KBManager {
	return &KBManager{indexer: indexer, paths: paths}
}

// Views returns a snapshot of the mounted knowledge bases. Each
// entry is a path; the Files / Size fields are best-effort stats
// derived from the indexer (Files is always 0 for now, the indexer
// doesn't expose a per-path file count without a scan).
func (m *KBManager) Views() []KBView {
	out := make([]KBView, 0, len(m.paths))
	for _, p := range m.paths {
		out = append(out, KBView{Path: p})
	}
	return out
}

// PrintList prints the mounted knowledge bases to stdout.
func (m *KBManager) PrintList() error {
	if len(m.paths) == 0 {
		color.HiBlack("  (未挂载任何知识库)")
		color.HiBlack("  用法: /kb add <目录路径>")
		return nil
	}
	fmt.Println()
	color.Cyan("  已挂载知识库 (%d):", len(m.paths))
	for _, p := range m.paths {
		fmt.Printf("    • %s\n", p)
	}
	fmt.Println()
	color.HiBlack("  用法: /kb scan   重新索引全部")
	return nil
}

func (m *KBManager) AddPath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		color.Red("  用法: /kb add <目录路径>")
		return nil
	}
	for _, existing := range m.paths {
		if existing == p {
			color.Yellow("  ⚠ 已存在: %s", p)
			return nil
		}
	}
	m.paths = append(m.paths, p)
	color.Green("  ✓ 已挂载: %s (运行 /kb scan 索引)", p)
	return nil
}

func (m *KBManager) RemovePath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		color.Red("  用法: /kb remove <目录路径>")
		return nil
	}
	for i, existing := range m.paths {
		if existing == p {
			m.paths = append(m.paths[:i], m.paths[i+1:]...)
			color.Green("  ✓ 已卸载: %s", p)
			return nil
		}
	}
	color.Yellow("  ⚠ 未找到: %s", p)
	return nil
}

func (m *KBManager) Scan(args string) error {
	added, updated, err := m.ScanStats()
	_ = added
	_ = updated
	if err != nil {
		return err
	}
	return nil
}

// ScanStats re-indexes every mounted KB and returns (added, updated,
// error). The two counters are aggregated across all paths and are
// best-effort: the indexer doesn't currently report per-path
// deltas, so both are reported as the total chunks indexed.
func (m *KBManager) ScanStats() (int, int, error) {
	if len(m.paths) == 0 {
		color.HiBlack("  (未挂载任何知识库)")
		return 0, 0, nil
	}
	total := 0
	for _, p := range m.paths {
		n, err := m.indexer.IndexDir(context.Background(), p)
		if err != nil {
			color.Red("  ✗ %s: %v", p, err)
			continue
		}
		fmt.Printf("  ✓ %s : %d chunks\n", p, n)
		total += n
	}
	return total, 0, nil
}
