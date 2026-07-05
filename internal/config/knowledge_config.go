package config

import "fmt"

// UpdateKnowledgeConfig merges a partial KnowledgeConfig blob (from a JSON
// PATCH body) into the persisted config. The patch is applied selectively:
// only non-zero/defined fields overwrite the existing value. The modified
// config is saved atomically to ~/.p-chat/config.json.
func UpdateKnowledgeConfig(patch KnowledgeConfig) (*KnowledgeConfig, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}

	kc := &cfg.Knowledge
	kc.Enabled = patch.Enabled
	kc.AutoIndex = patch.AutoIndex
	if len(patch.Bases) > 0 {
		kc.Bases = patch.Bases
	}

	cfg.Knowledge = *kc

	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, fmt.Errorf("save knowledge config: %w", err)
	}
	return &cfg.Knowledge, nil
}

// AddKnowledgeBaseRecord appends a knowledge base config and saves.
func AddKnowledgeBaseRecord(base KnowledgeBase) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}
	for _, b := range cfg.Knowledge.Bases {
		if b.Name == base.Name {
			return fmt.Errorf("knowledge base %q already exists", base.Name)
		}
	}
	cfg.Knowledge.Bases = append(cfg.Knowledge.Bases, base)
	mgr := NewManager()
	return mgr.SaveGlobal(cfg)
}

// RemoveKnowledgeBaseRecord deletes a knowledge base config by name and saves.
func RemoveKnowledgeBaseRecord(name string) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}
	bases := cfg.Knowledge.Bases
	idx := -1
	for i, b := range bases {
		if b.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("knowledge base %q not found", name)
	}
	cfg.Knowledge.Bases = append(bases[:idx], bases[idx+1:]...)
	mgr := NewManager()
	return mgr.SaveGlobal(cfg)
}
