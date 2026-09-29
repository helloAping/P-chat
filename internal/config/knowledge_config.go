package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// KnowledgeConfigPatch 表示知识库的局部更新。KnowledgeConfigPatch is a
// partial update for KnowledgeConfig. Pointer fields distinguish omitted
// values from explicit false or an explicit empty bases list.
type KnowledgeConfigPatch struct {
	Enabled   *bool            `json:"enabled,omitempty"`
	AutoIndex *bool            `json:"auto_index,omitempty"`
	Bases     *[]KnowledgeBase `json:"bases,omitempty"`
}

// UpdateKnowledgeConfig merges a partial KnowledgeConfigPatch into the
// persisted config. Only fields explicitly sent in the JSON body overwrite
// the existing value. The modified config is saved to ~/.p-chat/config.json.
func UpdateKnowledgeConfig(patch KnowledgeConfigPatch) (*KnowledgeConfig, error) {
	return UpdateKnowledgeConfigAt("", patch)
}

// UpdateKnowledgeConfigAt 按活动配置路径更新知识库配置。UpdateKnowledgeConfigAt
// updates the knowledge section at the active config path. An empty path keeps
// the established global-config behavior. A custom path is patched as an
// overlay: only explicitly supplied knowledge fields are written, so unrelated
// values inherited from global/project layers are not materialized or frozen.
func UpdateKnowledgeConfigAt(configPath string, patch KnowledgeConfigPatch) (*KnowledgeConfig, error) {
	if configPath != "" {
		if err := patchKnowledgeOverlay(configPath, patch); err != nil {
			return nil, fmt.Errorf("save knowledge config overlay: %w", err)
		}
		cfg, err := Load(configPath)
		if err != nil {
			return nil, fmt.Errorf("reload knowledge config overlay: %w", err)
		}
		return &cfg.Knowledge, nil
	}

	cfg, err := Load("")
	if err != nil {
		return nil, fmt.Errorf("load knowledge config: %w", err)
	}

	applyKnowledgePatch(&cfg.Knowledge, patch)

	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, fmt.Errorf("save knowledge config: %w", err)
	}
	return &cfg.Knowledge, nil
}

func applyKnowledgePatch(target *KnowledgeConfig, patch KnowledgeConfigPatch) {
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
	if patch.AutoIndex != nil {
		target.AutoIndex = *patch.AutoIndex
	}
	if patch.Bases != nil {
		target.Bases = append([]KnowledgeBase(nil), (*patch.Bases)...)
	}
}

// patchKnowledgeOverlay 只更新 custom 文件中显式提交的 knowledge 字段。
// patchKnowledgeOverlay updates only explicitly supplied knowledge fields in a
// custom config, preserving all other raw overlay fields and inheritance.
func patchKnowledgeOverlay(configPath string, patch KnowledgeConfigPatch) error {
	root := make(map[string]json.RawMessage)
	data, err := os.ReadFile(configPath)
	if err == nil {
		data = stripBOM(data)
		if _, _, err := tryUnmarshalWithTolerance(data, &root); err != nil {
			return fmt.Errorf("parse config %s: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", configPath, err)
	}
	if root == nil {
		root = make(map[string]json.RawMessage)
	}

	knowledgeLayer := make(map[string]json.RawMessage)
	if raw, ok := root["knowledge"]; ok && strings.TrimSpace(string(raw)) != "" && strings.TrimSpace(string(raw)) != "null" {
		if err := json.Unmarshal(raw, &knowledgeLayer); err != nil {
			return fmt.Errorf("parse knowledge config %s: %w", configPath, err)
		}
	}
	if knowledgeLayer == nil {
		knowledgeLayer = make(map[string]json.RawMessage)
	}

	setField := func(name string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal knowledge field %s: %w", name, err)
		}
		knowledgeLayer[name] = raw
		return nil
	}
	if patch.Enabled != nil {
		if err := setField("enabled", *patch.Enabled); err != nil {
			return err
		}
	}
	if patch.AutoIndex != nil {
		if err := setField("auto_index", *patch.AutoIndex); err != nil {
			return err
		}
	}
	if patch.Bases != nil {
		bases := append([]KnowledgeBase{}, (*patch.Bases)...)
		if err := setField("bases", bases); err != nil {
			return err
		}
	}

	knowledgeRaw, err := json.Marshal(knowledgeLayer)
	if err != nil {
		return fmt.Errorf("marshal knowledge config: %w", err)
	}
	root["knowledge"] = knowledgeRaw
	if err := writeJSONAtomic(configPath, root); err != nil {
		return fmt.Errorf("write config %s: %w", configPath, err)
	}
	return nil
}

// AddKnowledgeBaseRecord appends a knowledge base config and saves.
func AddKnowledgeBaseRecord(base KnowledgeBase) error {
	return AddKnowledgeBaseRecordAt("", base)
}

// AddKnowledgeBaseRecordAt 在活动配置路径追加知识库，并保留自定义配置的 overlay 语义。
// AddKnowledgeBaseRecordAt appends a knowledge base at the active config path
// while preserving custom-config overlay semantics.
func AddKnowledgeBaseRecordAt(configPath string, base KnowledgeBase) error {
	cfg, err := Load(configPath)
	if err != nil {
		return err
	}
	// Validate name. A blank name would create a base that
	// can't be referenced, and a name with path separators
	// or NUL bytes would break the SQLite filename convention.
	if base.Name == "" {
		return fmt.Errorf("knowledge base name is required")
	}
	if strings.ContainsAny(base.Name, `/\`+"`") || strings.ContainsRune(base.Name, 0) {
		return fmt.Errorf("knowledge base name %q contains invalid characters (no path separators or NUL)", base.Name)
	}
	if base.Path == "" {
		return fmt.Errorf("knowledge base path is required")
	}
	for _, b := range cfg.Knowledge.Bases {
		if b.Name == base.Name {
			return fmt.Errorf("knowledge base %q already exists", base.Name)
		}
	}
	bases := append(append([]KnowledgeBase(nil), cfg.Knowledge.Bases...), base)
	_, err = UpdateKnowledgeConfigAt(configPath, KnowledgeConfigPatch{Bases: &bases})
	return err
}

// RemoveKnowledgeBaseRecord deletes a knowledge base config by name and saves.
func RemoveKnowledgeBaseRecord(name string) error {
	return RemoveKnowledgeBaseRecordAt("", name)
}

// RemoveKnowledgeBaseRecordAt 从活动配置路径删除知识库，并保留未知及继承字段。
// RemoveKnowledgeBaseRecordAt deletes a knowledge base at the active config
// path while preserving unknown and inherited fields.
func RemoveKnowledgeBaseRecordAt(configPath, name string) error {
	cfg, err := Load(configPath)
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
	updatedBases := append([]KnowledgeBase(nil), bases[:idx]...)
	updatedBases = append(updatedBases, bases[idx+1:]...)
	_, err = UpdateKnowledgeConfigAt(configPath, KnowledgeConfigPatch{Bases: &updatedBases})
	return err
}
