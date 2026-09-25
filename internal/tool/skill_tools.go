package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/skill"
)

type skillToolArgs struct {
	Action   string `json:"action"`
	Name     string `json:"name,omitempty"`
	Resource string `json:"resource,omitempty"`
}

type skillManageArgs struct {
	Action     string `json:"action"`
	Name       string `json:"name,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	SourceURL  string `json:"source_url,omitempty"`
	Scope      string `json:"scope,omitempty"`
}

// RegisterSkillTools 注册两个合并后的 Skill 工具；传入 nil 时使用进程默认文件系统 Manager。
// RegisterSkillTools registers the two consolidated Skill tools; nil uses the process-default Manager.
func RegisterSkillTools(registry *Registry, manager skill.Manager) {
	if registry == nil {
		return
	}
	if manager == nil {
		manager = skill.NewManager()
	}
	registry.Register(Tool{
		Name: "skill",
		Description: "Discover and load installed Skill packages. Use action=load before following a Skill. " +
			"P-Chat will explicitly tell the user '当前调用 Skill：<name>' whenever a Skill is loaded.",
		Parameters: ObjectSchema(map[string]any{
			"action":   StringEnumProp("Operation to perform", "list", "inspect", "load", "read_resource", "doctor"),
			"name":     StringProp("Skill name; required for inspect, load, and read_resource"),
			"resource": StringProp("Package-relative resource path; required for read_resource"),
		}, []string{"action"}),
	}, makeSkillToolHandler(manager))

	registry.Register(Tool{
		Name:        "skill_manage",
		Description: "Install complete Skill packages from GitHub, import a local Skill package or collection directory exported by any external tool, or remove managed packages. This changes installed capabilities and requires user confirmation.",
		Parameters: ObjectSchema(map[string]any{
			"action":      StringEnumProp("Management operation", "install", "import", "remove"),
			"name":        StringProp("Skill name; required for remove, optional for collection import to select it and its declared dependencies"),
			"source_path": StringProp("Local Skill package directory or collection directory; required for import"),
			"source_url":  StringProp("HTTPS GitHub repository or Skill directory URL; required for install"),
			"scope":       StringEnumProp("Install scope (default global)", "global", "project"),
		}, []string{"action"}),
	}, makeSkillManageHandler(manager))
}

func makeSkillToolHandler(manager skill.Manager) ToolHandler {
	return func(ctx context.Context, raw json.RawMessage) (*CallResult, error) {
		var args skillToolArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return skillToolError("invalid arguments: " + err.Error()), nil
		}
		args.Action = strings.ToLower(strings.TrimSpace(args.Action))
		args.Name = strings.TrimSpace(args.Name)
		projectRoot := projectRootFromCtx(ctx)
		switch args.Action {
		case "list", "doctor":
			catalog, err := manager.Catalog(ctx, skill.CatalogQuery{ProjectRoot: projectRoot})
			if err != nil {
				return skillToolError(err.Error()), nil
			}
			data, _ := json.MarshalIndent(catalog, "", "  ")
			return &CallResult{Content: string(data), Summary: fmt.Sprintf("Found %d effective Skills", len(catalog.Skills))}, nil
		case "inspect":
			catalog, err := manager.Catalog(ctx, skill.CatalogQuery{ProjectRoot: projectRoot})
			if err != nil {
				return skillToolError(err.Error()), nil
			}
			for _, item := range catalog.Skills {
				if item.Name == args.Name {
					data, _ := json.MarshalIndent(item, "", "  ")
					return &CallResult{Content: string(data), Summary: "Inspected Skill " + args.Name}, nil
				}
			}
			return skillToolError(fmt.Sprintf("Skill %q is not installed", args.Name)), nil
		case "load":
			loaded, err := manager.Load(ctx, skill.LoadRequest{ProjectRoot: projectRoot, Name: args.Name})
			if err != nil {
				return &CallResult{
					Content: err.Error(), IsError: true,
					SkillInvocation: &SkillInvocation{Name: args.Name, Status: "error", Error: err.Error()},
				}, nil
			}
			if len(loaded.Skills) == 0 {
				message := fmt.Sprintf("Skill %q returned no instructions", args.Name)
				return &CallResult{
					Content: message, IsError: true,
					SkillInvocation: &SkillInvocation{Name: args.Name, Status: "error", Error: message},
				}, nil
			}
			primary := loaded.Skills[len(loaded.Skills)-1]
			dependencies := make([]string, 0, len(loaded.Skills)-1)
			for _, entry := range loaded.Skills[:len(loaded.Skills)-1] {
				dependencies = append(dependencies, entry.Name)
			}
			return &CallResult{
				Content: loaded.Context,
				Summary: "Loaded Skill " + loaded.Name,
				SkillInvocation: &SkillInvocation{
					Name: loaded.Name, Status: "ready", Scope: string(primary.Scope), Source: primary.Path,
					Dependencies: dependencies,
				},
			}, nil
		case "read_resource":
			loaded, err := manager.Load(ctx, skill.LoadRequest{ProjectRoot: projectRoot, Name: args.Name, Resource: args.Resource})
			if err != nil {
				return skillToolError(err.Error()), nil
			}
			return &CallResult{Content: loaded.ResourceContent, Summary: fmt.Sprintf("Read %s from Skill %s", loaded.Resource, args.Name)}, nil
		default:
			return skillToolError(fmt.Sprintf("unsupported skill action %q", args.Action)), nil
		}
	}
}

func makeSkillManageHandler(manager skill.Manager) ToolHandler {
	return func(ctx context.Context, raw json.RawMessage) (*CallResult, error) {
		var args skillManageArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return skillToolError("invalid arguments: " + err.Error()), nil
		}
		scope := skill.ScopeGlobalManaged
		if args.Scope == "project" {
			scope = skill.ScopeProjectManaged
		} else if args.Scope != "" && args.Scope != "global" {
			return skillToolError(`scope must be "global" or "project"`), nil
		}
		request := skill.ChangeRequest{Scope: scope, ProjectRoot: projectRootFromCtx(ctx)}
		switch strings.ToLower(strings.TrimSpace(args.Action)) {
		case "install":
			if strings.TrimSpace(args.SourceURL) == "" {
				return skillToolError("source_url is required for install"), nil
			}
			request.Action = skill.ChangeInstall
			request.SourceURL = args.SourceURL
			request.Name = args.Name
		case "import":
			if strings.TrimSpace(args.SourcePath) == "" {
				return skillToolError("source_path is required for import"), nil
			}
			request.Action = skill.ChangeImport
			request.SourcePath = args.SourcePath
			request.Name = args.Name
		case "remove":
			request.Action = skill.ChangeRemove
			request.Name = args.Name
		default:
			return skillToolError(fmt.Sprintf("unsupported skill_manage action %q", args.Action)), nil
		}
		result, err := manager.Apply(ctx, request)
		if err != nil {
			return skillToolError(err.Error()), nil
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		summary := fmt.Sprintf("Skill %s %s and verified", result.Name, args.Action)
		changedPaths := []string{result.Directory}
		if !result.Ready && request.Action != skill.ChangeRemove {
			if result.RolledBack {
				summary = fmt.Sprintf("Skill %s failed verification and the import was rolled back; inspect diagnostics", result.Name)
				changedPaths = nil
			} else {
				summary = fmt.Sprintf("Skill %s was copied but is not ready; inspect diagnostics", result.Name)
			}
		}
		return &CallResult{
			Content: string(data), Summary: summary, IsError: !result.Ready && request.Action != skill.ChangeRemove,
			ChangedPaths: changedPaths,
		}, nil
	}
}

func skillToolError(message string) *CallResult {
	return &CallResult{Content: message, IsError: true, Summary: message}
}
