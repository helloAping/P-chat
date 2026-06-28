package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type CallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type CallResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// SandboxChecker is a minimal interface satisfied by internal/sandbox.
// The tool package depends on this interface only (not on sandbox
// itself) to keep the dependency graph acyclic.
type SandboxChecker interface {
	// CheckExecBool returns true if the command is permitted to run.
	CheckExecBool(command string) bool
	// CheckWriteBool returns true if the path is permitted to be written.
	CheckWriteBool(path string) bool
}

type sandboxKey struct{}

// WithSandbox stores a SandboxChecker in ctx. The agent's tool
// dispatcher sets it before calling any tool handler.
func WithSandbox(ctx context.Context, s SandboxChecker) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, sandboxKey{}, s)
}

// sandboxFromCtx extracts the SandboxChecker, or returns nil.
func sandboxFromCtx(ctx context.Context) SandboxChecker {
	if v, ok := ctx.Value(sandboxKey{}).(SandboxChecker); ok {
		return v
	}
	return nil
}

type Registry struct {
	tools map[string]ToolHandler
	meta  map[string]Tool
}

type ToolHandler func(ctx context.Context, args json.RawMessage) (*CallResult, error)

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]ToolHandler),
		meta:  make(map[string]Tool),
	}
}

func (r *Registry) Register(t Tool, h ToolHandler) {
	r.tools[t.Name] = h
	r.meta[t.Name] = t
}

func (r *Registry) Get(name string) (ToolHandler, bool) {
	h, ok := r.tools[name]
	return h, ok
}

// Lookup returns the tool metadata, handler, and a found flag. Useful when
// you need both the description and the handler (e.g. to clone a registry
// without a specific tool).
func (r *Registry) Lookup(name string) (Tool, ToolHandler, bool) {
	h, hok := r.tools[name]
	t, tok := r.meta[name]
	if !hok || !tok {
		return Tool{}, nil, false
	}
	return t, h, true
}

// Names returns the registered tool names in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.meta))
	for name := range r.meta {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) List() []Tool {
	tools := make([]Tool, 0, len(r.meta))
	for _, t := range r.meta {
		tools = append(tools, t)
	}
	// Sort by name for byte-stable output (LLM cache stability).
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	return tools
}

// ObjectSchema is a helper to build a JSON-schema-like parameter object.
func ObjectSchema(props map[string]any, required []string) json.RawMessage {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	data, _ := json.Marshal(schema)
	return data
}

func StringProp(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func StringEnumProp(description string, values ...string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
}

// RegisterBuiltin registers the built-in tools.
func RegisterBuiltin(r *Registry) {
	r.Register(Tool{
		Name:        "exec_command",
		Description: "Execute a shell command on the local system. Returns stdout+stderr combined. Use for running scripts, system operations, and one-off commands.",
		Parameters: ObjectSchema(map[string]any{
			"command":   StringProp("The shell command to execute (cmd.exe syntax on Windows)"),
			"work_dir":  StringProp("Optional working directory for the command"),
		}, []string{"command"}),
	}, handleExecCommand)

	r.Register(Tool{
		Name:        "read_file",
		Description: "Read the full contents of a TEXT file. Use for inspecting source files, configs, or any text artifact. " +
			"DO NOT call read_file on images, audio, video, PDFs, archives, or any binary file — " +
			"those will return a binary error. " +
			"Images uploaded by the user are ALREADY available as vision input (image_url) in the user message; " +
			"just look at them directly, do NOT call read_file on the on-disk copy.",
		Parameters: ObjectSchema(map[string]any{
			"path": StringProp("Absolute or relative path to the file"),
		}, []string{"path"}),
	}, handleReadFile)

	r.Register(Tool{
		Name:        "write_file",
		Description: "Write (overwrite or create) a text file with the given content. Creates parent directories if needed.",
		Parameters: ObjectSchema(map[string]any{
			"path":    StringProp("Absolute or relative path to the file"),
			"content": StringProp("The full text content to write to the file"),
		}, []string{"path", "content"}),
	}, handleWriteFile)

	r.Register(Tool{
		Name:        "read_docx",
		Description: "Extract and return the full plain text content from a .docx (Word) file. Use this for reading Word documents uploaded by the user. Returns the document text as a single string.",
		Parameters: ObjectSchema(map[string]any{
			"path": StringProp("Absolute or relative path to the .docx file"),
		}, []string{"path"}),
	}, handleReadDocx)

	r.Register(Tool{
		Name:        "read_pdf",
		Description: "Extract and return the full plain text content from a .pdf file. Use this for reading PDF documents uploaded by the user. Returns the document text as a single string.",
		Parameters: ObjectSchema(map[string]any{
			"path": StringProp("Absolute or relative path to the .pdf file"),
		}, []string{"path"}),
	}, handleReadPdf)

	r.Register(Tool{
		Name:        "list_files",
		Description: "List files and subdirectories in a directory. Returns names only, not recursive.",
		Parameters: ObjectSchema(map[string]any{
			"path": StringProp("Directory path; pass '.' or empty for the current working directory"),
		}, []string{"path"}),
	}, handleListFiles)

	r.Register(Tool{
		Name:        "todo_write",
		Description: "Create and manage a structured task list for your current coding session. Use this to plan work, track progress, and show the user what you're doing. Each todo item has an id, content, and status (pending/in_progress/done/cancelled). Always include the full list when calling this tool — it replaces the previous list entirely.",
		Parameters: ObjectSchema(map[string]any{
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "string", "description": "Unique identifier for this todo item"},
						"content": map[string]any{"type": "string", "description": "The task description"},
						"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done", "cancelled"}, "description": "Current status"},
					},
					"required": []string{"id", "content", "status"},
				},
			},
		}, []string{"todos"}),
	}, handleTodoWrite)
}

type execArgs struct {
	Command string `json:"command"`
	WorkDir string `json:"work_dir,omitempty"`
}

func handleExecCommand(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a execArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Command == "" {
		return &CallResult{Content: "command is required", IsError: true}, nil
	}

	if sb := sandboxFromCtx(ctx); sb != nil && !sb.CheckExecBool(a.Command) {
		return &CallResult{
			Content: fmt.Sprintf("E_SANDBOX: command blocked by sandbox policy\n  command: %s\n  tip: use /unsafe once to allow this single call, or set sandbox.exec_dangerous_patterns in config", a.Command),
			IsError: true,
		}, nil
	}

	cmd := exec.CommandContext(ctx, "cmd", "/C", a.Command)
	if a.WorkDir != "" {
		cmd.Dir = a.WorkDir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return &CallResult{Content: fmt.Sprintf("%s\nERROR: %v", string(out), err), IsError: true}, nil
	}
	return &CallResult{Content: string(out)}, nil
}

type readFileArgs struct {
	Path string `json:"path"`
}

func handleReadFile(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a readFileArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Path == "" {
		return &CallResult{Content: "path is required", IsError: true}, nil
	}

	// Block access to the upload directory. Uploaded files are
	// images/audio/etc. that the user already attached to the
	// chat as vision/audio content; reading them back from disk
	// is pointless and confuses the LLM into thinking the model
	// doesn't support the content type.
	if isInUploadDir(a.Path) {
		return &CallResult{
			Content: fmt.Sprintf(
				"E_UPLOAD_DIR: read blocked — %s is inside the chat upload directory. "+
					"Uploaded files are already inlined in the user message as vision/image "+
					"content; do NOT call read_file on them. Just respond based on the "+
					"attachment you already received.", a.Path),
			IsError: true,
		}, nil
	}

	data, err := readFileForTool(a.Path)
	if err != nil {
		// Binary files get a clearer message than the bare
		// error. Everything else uses the normal error
		// channel so the LLM can react.
		if errors.Is(err, ErrBinaryFile) {
			return &CallResult{
				Content: err.Error() + "\nHint: if the user uploaded this file as an attachment, " +
					"the chat UI already inlines it as a multimodal part when the model " +
					"supports vision. Otherwise ask the user to switch to a vision-capable " +
					"model or paste the relevant text manually.",
				IsError: true,
			}, nil
		}
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{Content: string(data)}, nil
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleWriteFile(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a writeFileArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Path == "" {
		return &CallResult{Content: "path is required", IsError: true}, nil
	}

	if sb := sandboxFromCtx(ctx); sb != nil && !sb.CheckWriteBool(a.Path) {
		return &CallResult{
			Content: fmt.Sprintf("E_SANDBOX: write blocked by sandbox policy\n  path: %s\n  tip: this path is in the protected list; remove it from sandbox.write_protected_paths if you really mean it", a.Path),
			IsError: true,
		}, nil
	}

	if err := writeFile(a.Path, []byte(a.Content)); err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{Content: fmt.Sprintf("written %d bytes to %s", len(a.Content), a.Path)}, nil
}

type listFilesArgs struct {
	Path string `json:"path"`
}

func handleListFiles(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a listFilesArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Path == "" {
		a.Path = "."
	}

	entries, err := listDir(a.Path)
	if err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{Content: entries}, nil
}

// isInUploadDir reports whether the given file path lives inside
// the chat upload directory. Uploaded files (images, audio, etc.)
// are already inlined in the user message as multimodal content;
// the read_file tool is never the right way to access them and
// calling it on an uploaded image produces a confusing "model
// doesn't support image input" reply.
//
// The check is intentionally broad: absolute paths, relative paths
// (resolved against CWD), and bare filenames that match an
// uploaded file are all rejected. The LLM in practice uses
// whatever the user's last turn mentioned, which is often a bare
// filename like "image.png".
func isInUploadDir(p string) bool {
	if p == "" {
		return false
	}
	upDir := filepath.Clean(paths.GlobalDir() + string(filepath.Separator) + "uploads")

	// 1. Absolute path that lives under the upload dir.
	if filepath.IsAbs(p) {
		return strings.HasPrefix(filepath.Clean(p), upDir)
	}

	// 2. Bare filename ("image.png", "foo/bar.png", etc.) — match
	//    against the on-disk uploads directory listing. If a file
	//    with the same name was uploaded, reject.
	//    We strip any leading "./" and trim to the base name to
	//    keep the check cheap.
	cleaned := filepath.Clean(p)
	if entries, err := os.ReadDir(upDir); err == nil {
		// Match by full suffix path (foo.png, sub/foo.png) AND
		// by base name when the model asks for just "image.png".
		base := filepath.Base(cleaned)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if e.Name() == cleaned || e.Name() == base {
				return true
			}
		}
	}

	// 3. Path contains a separator — try resolving it relative
	//    to the upload dir. If it lands inside, reject.
	if strings.Contains(p, string(filepath.Separator)) {
		tryPath := filepath.Join(upDir, cleaned)
		if _, err := os.Stat(tryPath); err == nil {
			return true
		}
	}

	return false
}

// handleReadDocx is the tool handler for read_docx.
func handleReadDocx(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a readFileArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Path == "" {
		return &CallResult{Content: "path is required", IsError: true}, nil
	}
	text, err := readDocx(a.Path)
	if err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{Content: text}, nil
}

// handleReadPdf is the tool handler for read_pdf.
func handleReadPdf(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a readFileArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Path == "" {
		return &CallResult{Content: "path is required", IsError: true}, nil
	}
	text, err := readPdf(a.Path)
	if err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{Content: text}, nil
}
