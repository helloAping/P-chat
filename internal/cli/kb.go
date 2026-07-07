package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
)

// KBManager manages knowledge bases for the CLI REPL.
type KBManager struct {
	cfg *config.Config
}

// NewKBManager creates a KBManager backed by the active config.
func NewKBManager(cfg *config.Config) *KBManager {
	return &KBManager{cfg: cfg}
}

// Views returns the current knowledge base entries with section counts.
func (m *KBManager) Views() []KBView {
	if m.cfg == nil {
		return nil
	}
	var out []KBView
	for _, b := range m.cfg.Knowledge.Bases {
		v := KBView{Path: b.Path}
		store, err := knowledge.GetOrOpenWikiStore(b.Name, b.Path)
		if err == nil {
			v.Files = store.CountNodes(context.Background(), b.Name)
		}
		if b.Enabled {
			v.Size = 1
		}
		out = append(out, v)
	}
	return out
}

// AddPath adds a directory as a new knowledge base. Enables the knowledge
// system if it was disabled.
func (m *KBManager) AddPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}
	name := filepath.Base(abs)
	enabled := true
	patch := config.KnowledgeConfigPatch{}
	patch.Enabled = &enabled
	patch.Bases = append(m.cfg.Knowledge.Bases, config.KnowledgeBase{
		Name:    name,
		Path:    abs,
		Enabled: true,
	})
	if _, err := config.UpdateKnowledgeConfig(patch); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	m.cfg.Knowledge.Enabled = true
	m.cfg.Knowledge.Bases = append(m.cfg.Knowledge.Bases, patch.Bases[0])
	return nil
}

// RemovePath removes a knowledge base by its directory path.
func (m *KBManager) RemovePath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	idx := -1
	for i, b := range m.cfg.Knowledge.Bases {
		if b.Path == abs || (len(b.Name) > 0 && strings.HasSuffix(abs, b.Name)) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("knowledge base at %q not found", abs)
	}
	if err := config.RemoveKnowledgeBaseRecord(m.cfg.Knowledge.Bases[idx].Name); err != nil {
		return err
	}
	m.cfg.Knowledge.Bases = append(m.cfg.Knowledge.Bases[:idx], m.cfg.Knowledge.Bases[idx+1:]...)
	return nil
}

// ScanStats returns the total section count across all bases.
func (m *KBManager) ScanStats() (added, updated int, err error) {
	if m.cfg == nil || !m.cfg.Knowledge.Enabled {
		return 0, 0, nil
	}
	for _, b := range m.cfg.Knowledge.Bases {
		if !b.Enabled {
			continue
		}
		store, storeErr := knowledge.GetOrOpenWikiStore(b.Name, b.Path)
		if storeErr != nil {
			continue
		}
		count := store.CountNodes(context.Background(), b.Name)
		added += count
	}
	return added, 0, nil
}

// Scan triggers a re-scan of all enabled knowledge bases via the wiki store.
func (m *KBManager) Scan(name string) error {
	if m.cfg == nil || !m.cfg.Knowledge.Enabled {
		return fmt.Errorf("knowledge base is disabled")
	}

	var bases []config.KnowledgeBase
	if name == "" || name == "__all__" {
		for _, b := range m.cfg.Knowledge.Bases {
			if b.Enabled {
				bases = append(bases, b)
			}
		}
	} else {
		for _, b := range m.cfg.Knowledge.Bases {
			if b.Name == name {
				bases = append(bases, b)
				break
			}
		}
	}
	if len(bases) == 0 {
		return fmt.Errorf("no enabled knowledge bases")
	}

	for _, base := range bases {
		log.Printf("[kb scan] scanning %s (%s)...", base.Name, base.Path)

		store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
		if err != nil {
			return fmt.Errorf("open wiki store %s: %w", base.Name, err)
		}

		// Count files first for progress.
		relFiles := countRelIndexableFiles(base.Path)
		log.Printf("[kb scan] %s: %d indexable files", base.Name, len(relFiles))

		// Walk and build index_nodes.
		var allNodes []knowledge.IndexNode
		var allContents []knowledge.ContentNode
		for i, rel := range relFiles {
			absPath := filepath.Join(base.Path, rel)
			text, readErr := knowledge.ReadFileText(absPath)
			if readErr != nil {
				log.Printf("[kb scan] %s: read error for %s: %v", base.Name, rel, readErr)
				continue
			}

			roots := knowledge.BuildHeadingTree(text, 3)
			seq := 0
			var walkNodes func([]*knowledge.HeadingNode)
			walkNodes = func(list []*knowledge.HeadingNode) {
				for _, node := range list {
					if !node.HasContent() {
						walkNodes(node.Children)
						continue
					}
					aggregated := node.AggregatedContent()
					allNodes = append(allNodes, knowledge.IndexNode{
						Level:     3,
						Source:    rel,
						Kind:      "text",
						SortOrder: seq,
						Title:     node.Title,
						Overview:  knowledge.TruncateText(aggregated, 500),
					})
					seq++
					allContents = append(allContents, knowledge.ContentNode{
						Content:     knowledge.TruncateText(aggregated, 3000),
						ContentType: "text",
						SortOrder:   0,
					})
					walkNodes(node.Children)
				}
			}
			walkNodes(roots)

			// Fallback: whole file as one node.
			if seq == 0 && text != "" {
				title := rel
				if idx := strings.LastIndex(rel, "/"); idx >= 0 {
					title = rel[idx+1:]
				}
				allNodes = append(allNodes, knowledge.IndexNode{
					Level:     3,
					Source:    rel,
					Kind:      "text",
					SortOrder: 0,
					Title:     title,
					Overview:  knowledge.TruncateText(text, 500),
				})
				allContents = append(allContents, knowledge.ContentNode{
					Content:     knowledge.TruncateText(text, 3000),
					ContentType: "text",
					SortOrder:   0,
				})
			}

			if (i+1)%10 == 0 {
				log.Printf("[kb scan] %s: %d/%d files", base.Name, i+1, len(relFiles))
			}
		}

		// Assign IDs and assemble L1/L2/L3 hierarchy.
		nodeID := 1
		l1 := knowledge.IndexNode{ID: nodeID, Level: 1, Base: base.Name, Title: base.Name, Source: base.Path}
		nodeID++

		seenFiles := map[string]int{}
		var nodes []knowledge.IndexNode
		nodes = append(nodes, l1)
		for _, n := range allNodes {
			fid, ok := seenFiles[n.Source]
			if !ok {
				fid = nodeID
				seenFiles[n.Source] = fid
				nodes = append(nodes, knowledge.IndexNode{
					ID:       fid,
					ParentID: l1.ID,
					Level:    2,
					Base:     base.Name,
					Source:   n.Source,
					Kind:     "text",
					Title:    n.Source,
				})
				nodeID++
			}
			n.ID = nodeID
			n.ParentID = fid
			n.Base = base.Name
			nodeID++
			nodes = append(nodes, n)
		}
		for i := range allContents {
			allContents[i].NodeID = allNodes[i].ID
		}

		if err := store.ReplaceBaseNodes(context.Background(), base.Name, nodes, allContents); err != nil {
			return fmt.Errorf("store %s: %w", base.Name, err)
		}

		l2Count := len(seenFiles)
		l3Count := len(allNodes)
		log.Printf("[kb scan] %s: %d L2 files, %d L3 sections", base.Name, l2Count, l3Count)
	}
	return nil
}



// countRelIndexableFiles walks a directory and returns relative paths of indexable files.
func countRelIndexableFiles(dir string) []string {
	var files []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 5*1024*1024 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if knowledge.IndexableExtensions[ext] {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}