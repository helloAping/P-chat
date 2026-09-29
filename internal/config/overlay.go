package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type configOverlayField struct {
	path  []string
	value any
}

func overlayField(value any, path ...string) configOverlayField {
	return configOverlayField{path: path, value: value}
}

// saveConfigFieldsAt 将有效配置的指定字段写回活动配置层。
// saveConfigFieldsAt writes selected effective-config fields back to the active
// configuration layer. A custom path is edited as raw JSON so inherited and
// unknown fields remain untouched.
func saveConfigFieldsAt(configPath string, cfg *Config, fields ...configOverlayField) error {
	if configPath == "" {
		return NewManager().SaveGlobal(cfg)
	}
	if len(fields) == 0 {
		return nil
	}

	root, err := readConfigOverlay(configPath)
	if err != nil {
		return err
	}
	for _, field := range fields {
		if len(field.path) == 0 {
			return fmt.Errorf("config overlay field path is empty")
		}
		if err := setConfigOverlayPath(root, field.path, field.value); err != nil {
			return err
		}
	}
	if err := writeJSONAtomic(configPath, root); err != nil {
		return fmt.Errorf("write config %s: %w", configPath, err)
	}
	return nil
}

func readConfigOverlay(configPath string) (map[string]json.RawMessage, error) {
	root := make(map[string]json.RawMessage)
	data, err := os.ReadFile(configPath)
	if err == nil {
		data = stripBOM(data)
		if _, _, err := tryUnmarshalWithTolerance(data, &root); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config %s: %w", configPath, err)
	}
	if root == nil {
		root = make(map[string]json.RawMessage)
	}
	return root, nil
}

func setConfigOverlayPath(object map[string]json.RawMessage, path []string, value any) error {
	name := path[0]
	if len(path) == 1 {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal config field %s: %w", strings.Join(path, "."), err)
		}
		object[name] = raw
		return nil
	}

	child := make(map[string]json.RawMessage)
	if raw, ok := object[name]; ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(raw, &child); err != nil {
				return fmt.Errorf("parse config object %s: %w", name, err)
			}
		}
	}
	if child == nil {
		child = make(map[string]json.RawMessage)
	}
	if err := setConfigOverlayPath(child, path[1:], value); err != nil {
		return err
	}
	raw, err := json.Marshal(child)
	if err != nil {
		return fmt.Errorf("marshal config object %s: %w", name, err)
	}
	object[name] = raw
	return nil
}

// prepareCustomOverlayLoad 在应用 custom 层前清空需要“完整替换”的 map。
// prepareCustomOverlayLoad clears maps with replacement semantics before the
// custom layer is unmarshaled. Ordinary maps retain Go's merge behavior.
func prepareCustomOverlayLoad(data []byte, cfg *Config) ([]byte, error) {
	root := make(map[string]json.RawMessage)
	cleaned, _, err := tryUnmarshalWithTolerance(data, &root)
	if err != nil {
		return nil, err
	}
	if configOverlayHasField(root, "generation", "defaults") {
		cfg.Generation.Defaults = nil
	}
	if configOverlayHasField(root, "im", "personas") {
		cfg.IM.Personas = nil
	}
	return cleaned, nil
}

func configOverlayHasField(root map[string]json.RawMessage, section, field string) bool {
	raw, ok := root[section]
	if !ok || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return false
	}
	object := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	_, ok = object[field]
	return ok
}

// UpdateBrowserEnabledAt 按活动配置路径更新浏览器总开关。
// UpdateBrowserEnabledAt updates the browser master switch at the active
// configuration path.
func UpdateBrowserEnabledAt(configPath string, enabled bool) (*BrowserConfig, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load browser config: %w", err)
	}
	cfg.Browser.Enabled = enabled
	if err := saveConfigFieldsAt(configPath, cfg, overlayField(enabled, "browser", "enabled")); err != nil {
		return nil, fmt.Errorf("save browser config: %w", err)
	}
	return &cfg.Browser, nil
}

// UpdateMCPEnabledAt 按活动配置路径更新 MCP 总开关。
// UpdateMCPEnabledAt updates the MCP master switch at the active path.
func UpdateMCPEnabledAt(configPath string, enabled bool) (*MCPConfig, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load mcp config: %w", err)
	}
	cfg.MCP.Enabled = enabled
	if err := saveConfigFieldsAt(configPath, cfg, overlayField(cfg.MCP.Enabled, "mcp", "enabled")); err != nil {
		return nil, fmt.Errorf("save mcp config: %w", err)
	}
	return &cfg.MCP, nil
}

// UpdateMCPServersAt 按活动配置路径替换 MCP 服务器列表。
// UpdateMCPServersAt replaces the MCP server list at the active path.
func UpdateMCPServersAt(configPath string, servers []MCPServerConfig) (*MCPConfig, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load mcp config: %w", err)
	}
	cfg.MCP.Servers = append([]MCPServerConfig{}, servers...)
	if err := saveConfigFieldsAt(configPath, cfg, overlayField(cfg.MCP.Servers, "mcp", "servers")); err != nil {
		return nil, fmt.Errorf("save mcp config: %w", err)
	}
	return &cfg.MCP, nil
}
