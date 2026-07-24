package tool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRegistry_ProjectDynamicOverridesGlobal(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltin(r)
	globalHandler := func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "global"}, nil
	}
	projectHandler := func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "project"}, nil
	}
	r.SetDynamicSnapshot(ToolOrigin{Scope: ToolOriginGlobal}, map[string]ToolEntryHandler{
		"shared": {
			Tool:    Tool{Name: "shared", Description: "global"},
			Handler: globalHandler,
			Origin:  ToolOrigin{Source: "global.yaml"},
		},
	})
	r.SetDynamicSnapshot(ToolOrigin{Scope: ToolOriginProject, ProjectRoot: "C:/repo/a"}, map[string]ToolEntryHandler{
		"shared": {
			Tool:    Tool{Name: "shared", Description: "project"},
			Handler: projectHandler,
			Origin:  ToolOrigin{Source: "project.yaml"},
		},
	})

	gotGlobal, hGlobal, ok := r.LookupForProject("shared", "")
	if !ok || gotGlobal.Description != "global" {
		t.Fatalf("global lookup = %+v ok=%v, want global", gotGlobal, ok)
	}
	res, err := hGlobal(context.Background(), nil)
	if err != nil || res.Content != "global" {
		t.Fatalf("global handler result = %+v err=%v, want global", res, err)
	}

	gotProject, hProject, ok := r.LookupForProject("shared", "C:/repo/a")
	if !ok || gotProject.Description != "project" {
		t.Fatalf("project lookup = %+v ok=%v, want project", gotProject, ok)
	}
	res, err = hProject(context.Background(), nil)
	if err != nil || res.Content != "project" {
		t.Fatalf("project handler result = %+v err=%v, want project", res, err)
	}

	r.ClearDynamicSnapshot(ToolOrigin{Scope: ToolOriginProject, ProjectRoot: "C:/repo/a"})
	gotAfterDelete, hAfterDelete, ok := r.LookupForProject("shared", "C:/repo/a")
	if !ok || gotAfterDelete.Description != "global" {
		t.Fatalf("after project delete lookup = %+v ok=%v, want global fallback", gotAfterDelete, ok)
	}
	res, err = hAfterDelete(context.Background(), nil)
	if err != nil || res.Content != "global" {
		t.Fatalf("after delete handler result = %+v err=%v, want global", res, err)
	}
}

func TestRegistry_ProjectDynamicDoesNotLeak(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltin(r)
	r.SetDynamicSnapshot(ToolOrigin{Scope: ToolOriginProject, ProjectRoot: "C:/repo/a"}, map[string]ToolEntryHandler{
		"project_only": {
			Tool: Tool{Name: "project_only", Description: "project only"},
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return &CallResult{Content: "ok"}, nil
			},
			Origin: ToolOrigin{Source: "project.yaml"},
		},
	})

	if _, _, ok := r.LookupForProject("project_only", ""); ok {
		t.Fatal("project-only tool leaked into global view")
	}
	if _, _, ok := r.LookupForProject("project_only", "C:/repo/b"); ok {
		t.Fatal("project-only tool leaked into a different project")
	}
	if _, _, ok := r.LookupForProject("project_only", "C:/repo/a"); !ok {
		t.Fatal("project-only tool missing from its project view")
	}
}
