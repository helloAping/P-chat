package tool

import (
	"context"
	"encoding/json"
	"strings"
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

func TestRegistry_RegisterClearsDynamicSourceForBuiltinOverride(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithSource(Tool{Name: "shared", Description: "dynamic"}, func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "dynamic"}, nil
	}, "shared.yaml")
	r.Register(Tool{Name: "shared", Description: "builtin"}, func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "builtin"}, nil
	})

	tool, handler, ok := r.Lookup("shared")
	if !ok {
		t.Fatal("shared should be registered")
	}
	if tool.Description != "builtin" {
		t.Fatalf("description = %q, want builtin", tool.Description)
	}
	if _, ok := r.SourceFile("shared"); ok {
		t.Fatal("builtin override should clear dynamic source marker")
	}
	res, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "builtin" {
		t.Fatalf("handler = %q, want builtin", res.Content)
	}
}

func TestRegistry_UnregisterBuiltinLeavesDynamicTools(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithSource(Tool{Name: "dynamic_only", Description: "dynamic"}, func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "dynamic"}, nil
	}, "dynamic.yaml")

	r.UnregisterBuiltin("dynamic_only")

	tool, handler, ok := r.Lookup("dynamic_only")
	if !ok {
		t.Fatal("dynamic tool should remain")
	}
	if tool.Description != "dynamic" {
		t.Fatalf("description = %q, want dynamic", tool.Description)
	}
	res, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "dynamic" {
		t.Fatalf("handler = %q, want dynamic", res.Content)
	}
}

func TestRegistry_AliasIsCallableButHiddenFromToolList(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "media_recognize", Description: "canonical"}, func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "recognized"}, nil
	})
	r.RegisterAlias("image_recognize", "media_recognize")

	if names := r.Names(); len(names) != 1 || names[0] != "media_recognize" {
		t.Fatalf("Names() = %v, want only the canonical tool", names)
	}
	if tools := r.List(); len(tools) != 1 || tools[0].Name != "media_recognize" {
		t.Fatalf("List() = %+v, want only the canonical tool", tools)
	}

	meta, handler, ok := r.Lookup("image_recognize")
	if !ok {
		t.Fatal("hidden compatibility alias should remain callable")
	}
	if meta.Name != "media_recognize" {
		t.Fatalf("alias metadata name = %q, want canonical name", meta.Name)
	}
	result, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recognized" {
		t.Fatalf("alias handler result = %q, want recognized", result.Content)
	}
}

func TestRegistry_ProjectDynamicToolOverridesHiddenAlias(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "media_recognize", Description: "canonical"}, func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "canonical"}, nil
	})
	r.RegisterAlias("image_recognize", "media_recognize")
	r.SetDynamicSnapshot(ToolOrigin{Scope: ToolOriginProject, ProjectRoot: "C:/repo/a"}, map[string]ToolEntryHandler{
		"image_recognize": {
			Tool: Tool{Name: "image_recognize", Description: "project override"},
			Handler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
				return &CallResult{Content: "project"}, nil
			},
			Origin: ToolOrigin{Source: "project.yaml"},
		},
	})

	meta, handler, ok := r.LookupForProject("image_recognize", "C:/repo/a")
	if !ok || meta.Name != "image_recognize" {
		t.Fatalf("project lookup = %+v ok=%v, want exact project tool", meta, ok)
	}
	result, err := handler(context.Background(), nil)
	if err != nil || result.Content != "project" {
		t.Fatalf("project handler result = %+v err=%v, want project", result, err)
	}

	meta, _, ok = r.Lookup("image_recognize")
	if !ok || meta.Name != "media_recognize" {
		t.Fatalf("global lookup = %+v ok=%v, want canonical alias fallback", meta, ok)
	}
}

func TestRegisterBuiltin_UsesMediaRecognitionAsCanonicalTool(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltin(r)

	for _, name := range r.Names() {
		if name == "image_recognize" {
			t.Fatal("image_recognize should be a hidden compatibility alias")
		}
	}
	meta, _, ok := r.Lookup("image_recognize")
	if !ok {
		t.Fatal("legacy image_recognize calls should remain callable")
	}
	if meta.Name != "media_recognize" {
		t.Fatalf("legacy alias resolves to %q, want media_recognize", meta.Name)
	}
}

func TestRegisterBuiltin_UsesReadFileAsCanonicalLocalReader(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltin(r)

	names := r.Names()
	for _, legacy := range []string{"read_docx", "read_pdf"} {
		for _, name := range names {
			if name == legacy {
				t.Fatalf("%s should be a hidden compatibility alias", legacy)
			}
		}
		meta, _, ok := r.Lookup(legacy)
		if !ok {
			t.Fatalf("legacy %s calls should remain callable", legacy)
		}
		if meta.Name != "read_file" {
			t.Fatalf("legacy %s resolves to %q, want read_file", legacy, meta.Name)
		}
	}
}

func TestRegisterBuiltin_HidesLegacyNamesFromModelContracts(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltin(r)
	payload, err := json.Marshal(r.List())
	if err != nil {
		t.Fatal(err)
	}
	contract := string(payload)
	for _, legacy := range []string{"image_recognize", "read_docx", "read_pdf", "start_process"} {
		if strings.Contains(contract, legacy) {
			t.Fatalf("model-facing tool contract still mentions hidden legacy name %q", legacy)
		}
	}
}

func TestRegistry_AliasCanAdaptLegacyArguments(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "exec_command", Description: "canonical"}, func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "canonical"}, nil
	})
	r.RegisterAliasHandler("start_process", "exec_command", func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
		return &CallResult{Content: "legacy adapter"}, nil
	})

	meta, handler, ok := r.Lookup("start_process")
	if !ok || meta.Name != "exec_command" {
		t.Fatalf("alias lookup = %+v ok=%v, want exec_command metadata", meta, ok)
	}
	result, err := handler(context.Background(), nil)
	if err != nil || result.Content != "legacy adapter" {
		t.Fatalf("alias adapter result = %+v err=%v", result, err)
	}
	if names := r.Names(); len(names) != 1 || names[0] != "exec_command" {
		t.Fatalf("Names() = %v, want only exec_command", names)
	}
}
