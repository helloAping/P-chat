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
			sections, _ := store.ListBase(context.Background(), b.Name)
			v.Files = len(sections)
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
		sections, _ := store.ListBase(context.Background(), b.Name)
		added += len(sections)
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
		files, sections, err := scanDir(context.Background(), store, base.Path, base.Name)
		if err != nil {
			return fmt.Errorf("scan %s: %w", base.Name, err)
		}
		log.Printf("[kb scan] %s: %d sections in %d files", base.Name, sections, files)
	}
	return nil
}

// scanDir walks a directory, parses text files into wiki sections,
// and stores them. Returns (fileCount, sectionCount, error).
func scanDir(ctx context.Context, store *knowledge.WikiStore, dir, baseName string) (int, int, error) {
	var processed, totalSections int
	currentSources := make(map[string]bool)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !knowledge.IndexableExtensions[ext] {
			return nil
		}
		if info.Size() > 5*1024*1024 {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		currentSources[rel] = true

		sections, parseErr := knowledge.ParseWikiFile(path, rel, 3)
		if parseErr != nil {
			return nil
		}
		for i := range sections {
			sections[i].Base = baseName
		}
		if err := store.ReplaceSource(ctx, baseName, rel, sections); err != nil {
			log.Printf("[kb scan] replace %s: %v", rel, err)
		}
		store.SetFileMtime(ctx, baseName, rel, info.ModTime().Unix())
		totalSections += len(sections)
		processed++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	if err := store.RemoveStaleSources(ctx, baseName, currentSources); err != nil {
		log.Printf("[kb scan] stale cleanup %s: %v", baseName, err)
	}
	return processed, totalSections, nil
}
