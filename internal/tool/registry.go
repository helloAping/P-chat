package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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
	// CheckExecDecision returns the full Decision (allow/block/confirm).
	CheckExecDecision(command string) SandboxDecision
	// CheckWriteDecision returns the full Decision for write.
	CheckWriteDecision(path string) SandboxDecision
	// MatchedPattern returns the regex pattern that matched, or "".
	MatchedPattern(command string) string
}

// SandboxDecision mirrors sandbox.Decision without importing sandbox.
type SandboxDecision int

const (
	SandboxAllow   SandboxDecision = 0
	SandboxBlock   SandboxDecision = 1
	SandboxConfirm SandboxDecision = 2
)

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

type projectRootKey struct{}

// WithProjectRoot stores the session's project root directory in ctx.
func WithProjectRoot(ctx context.Context, root string) context.Context {
	if root == "" {
		return ctx
	}
	return context.WithValue(ctx, projectRootKey{}, root)
}

// projectRootFromCtx extracts the project root, or returns "".
func projectRootFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(projectRootKey{}).(string); ok {
		return v
	}
	return ""
}

// resolveToProjectRoot resolves a path to an absolute path.
// If the path is relative, it is resolved against the project
// root from the context. If the path is already absolute or no
// project root is set, it is returned unchanged.
func resolveToProjectRoot(ctx context.Context, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if root := projectRootFromCtx(ctx); root != "" {
		return filepath.Join(root, p)
	}
	return p
}

type Registry struct {
	mu    sync.RWMutex
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
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = h
	r.meta[t.Name] = t
}

func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.meta, name)
}

func (r *Registry) Get(name string) (ToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.tools[name]
	return h, ok
}

// Lookup returns the tool metadata, handler, and a found flag. Useful when
// you need both the description and the handler (e.g. to clone a registry
// without a specific tool).
func (r *Registry) Lookup(name string) (Tool, ToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, hok := r.tools[name]
	t, tok := r.meta[name]
	if !hok || !tok {
		return Tool{}, nil, false
	}
	return t, h, true
}

// Names returns the registered tool names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.meta))
	for name := range r.meta {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
		Name:        "web_fetch",
		Description: "Fetch the content of a URL and return it as text. Use for reading online documentation, API responses, web pages, or any publicly accessible HTTP resource. The response is limited to 1 MB of text. Supports HTTP and HTTPS URLs only.",
		Parameters: ObjectSchema(map[string]any{
			"url":    StringProp("The full URL to fetch (must start with http:// or https://)"),
			"method": StringEnumProp("HTTP method to use (default GET)", "GET", "POST"),
			"body":   StringProp("Request body for POST requests (plain text or JSON)"),
		}, []string{"url"}),
	}, handleWebFetch)

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

	r.Register(Tool{
		Name:        "question",
		Description: "Ask the user a question (or set of questions) when you need clarification, a decision, or input before proceeding. Each question can have multiple-choice options or allow free-text input. Use this when you are uncertain about requirements, need to choose between approaches, or want the user to confirm a plan before executing. The user's answers will be returned so you can continue. Important: for every question, always include an '其他' (Other) option at the end so the user can provide a custom answer if none of the predefined options fit.",
		Parameters: ObjectSchema(map[string]any{
			"questions": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": 10,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question":     map[string]any{"type": "string", "description": "The complete question to ask the user"},
						"header":       map[string]any{"type": "string", "maxLength": 12, "description": "Short label (max 12 chars) shown as a chip/tag"},
						"options":      map[string]any{"type": "array", "minItems": 2, "maxItems": 8, "items": map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string", "description": "Display text (1-5 words)"}, "description": map[string]any{"type": "string", "description": "Explanation of this choice"}}, "required": []string{"label", "description"}}},
						"multi_select": map[string]any{"type": "boolean", "description": "Allow selecting multiple options (default false)"},
					},
					"required": []string{"question", "header", "options"},
				},
			},
		}, []string{"questions"}),
	}, handleQuestion)
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

	// Block commands that try to read uploaded files. The
	// LLM, when handed a vision-incompatible image, has
	// historically used `cat image.png` / `xxd image.png` /
	// `type image.png` to "see" the file. The bytes are
	// useless as text; the LLM then invents a "model doesn't
	// support image input" error message. read_file already
	// rejects upload-dir paths; we mirror that here so
	// exec_command is not an escape hatch.
	if reason := commandReferencesUploadFile(a.Command); reason != "" {
		return &CallResult{
			Content: fmt.Sprintf(
				"E_UPLOAD_DIR: command blocked — %s is inside the chat upload directory. "+
					"Uploaded files are already inlined in the user message as vision/image "+
					"content; do NOT shell out to read them. Just respond based on the "+
					"attachment you already received.", reason),
			IsError: true,
		}, nil
	}

	cmd := exec.CommandContext(ctx, "cmd", "/C", a.Command)
	root := projectRootFromCtx(ctx)
	if a.WorkDir != "" {
		if filepath.IsAbs(a.WorkDir) || root == "" {
			cmd.Dir = a.WorkDir
		} else {
			cmd.Dir = filepath.Join(root, a.WorkDir)
		}
	} else if root != "" {
		cmd.Dir = root
	}

	// PowerShell on Windows: bypass cmd /C to avoid:
	// 1. cmd.exe intercepting pipes (|) inside -Command "..."
	// 2. GBK output encoding (PowerShell 5.1 default on zh-CN)
	//
	// Original: powershell -NoProfile -Command "Get-Content ... | Select-Object ..."
	//   → cmd /C strips outer quotes → | interpreted by cmd.exe → parser error
	//
	// Fix: run powershell.exe directly with [Console]::OutputEncoding = UTF-8.
	//
	// Detection: scan ALL tokens for powershell|pwsh, not just the
	// first one. The LLM can bypass the prefix-only check by writing
	// "cmd /C powershell ..." or prepending whitespace, which would
	// otherwise fall through to `cmd /C powershell -Command "..."`
	// where cmd.exe misinterprets the inner quotes/pipes.
	if runtime.GOOS == "windows" {
		trimmed := strings.TrimSpace(a.Command)
		tokens := strings.Fields(trimmed)
		var psIdx int = -1
		var psExe string
		for i, t := range tokens {
			base := strings.ToLower(strings.TrimRight(t, ".exe"))
			if base == "powershell" || base == "pwsh" {
				psIdx = i
				psExe = base + ".exe"
				break
			}
		}
		if psIdx >= 0 {
			// Find -Command <script> after psIdx.
			cmdIdx := -1
			for j := psIdx + 1; j < len(tokens)-1; j++ {
				if strings.EqualFold(tokens[j], "-Command") || strings.EqualFold(tokens[j], "-c") {
					cmdIdx = j
					break
				}
			}
			if cmdIdx >= 0 && cmdIdx+1 < len(tokens) {
				flags := strings.Join(tokens[psIdx+1:cmdIdx], " ")
				script := strings.Join(tokens[cmdIdx+1:], " ")
				if (strings.HasPrefix(script, "\"") && strings.HasSuffix(script, "\"")) ||
					(strings.HasPrefix(script, "'") && strings.HasSuffix(script, "'")) {
					script = script[1 : len(script)-1]
				}
				script = fmt.Sprintf("[Console]::OutputEncoding = [Text.Encoding]::UTF8; %s", script)
				args := append([]string{}, strings.Fields(flags)...)
				args = append(args, "-Command", script)
				cmd = exec.CommandContext(ctx, psExe, args...)
			}
		}
	}

	out, err := readLimitedOutput(cmd)
	if err != nil {
		content := strings.TrimRight(string(out), "\r\n")
		if content != "" {
			content += "\n"
		}
		content += err.Error()

		// opcode-style actionable hints: when a command fails
		// because it doesn't exist on this platform, tell the
		// LLM about alternatives so it can recover.
		if runtime.GOOS == "windows" {
			switch {
			case strings.Contains(err.Error(), "is not recognized"):
				content += "\nHint: This command doesn't exist on Windows. Use findstr (not grep), dir (not ls), type (not cat), or pwsh -NoProfile -Command \"...\" for PowerShell scripts."
			case strings.Contains(err.Error(), "The system cannot find the file"):
				content += "\nHint: The executable was not found. Check the command name — on Windows the available commands are: dir, findstr, type, copy, move, del, mkdir, cd, set, pwsh."
			}
		} else {
			if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "command not found") {
				content += "\nHint: The command was not found. Try installing it or use an alternative."
			}
		}
		return &CallResult{Content: content, IsError: true}, nil
	}
	return &CallResult{Content: string(out)}, nil
}

const maxExecReadSize = 256 * 1024

// readLimitedOutput captures stdout+stderr with a hard byte cap
// to prevent memory exhaustion if the LLM runs an unbounded
// command (e.g. `cat /dev/zero`).
func readLimitedOutput(cmd *exec.Cmd) ([]byte, error) {
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 2)
	readPipe := func(r io.Reader) {
		data, err := io.ReadAll(io.LimitReader(r, maxExecReadSize))
		ch <- result{data, err}
	}
	go readPipe(stdout)
	go readPipe(stderr)

	r1, r2 := <-ch, <-ch
	waitErr := cmd.Wait()
	out := append(r1.data, r2.data...)

	// Surface every error. The previous code only returned
	// waitErr when there was no read error — meaning a read
	// error during a successful process was silently dropped
	// (the user would see partial output with no warning).
	switch {
	case waitErr != nil:
		// Include any concurrent read errors so the user can
		// tell whether output was truncated by I/O failure.
		return out, fmt.Errorf("%w (stdout read: %v, stderr read: %v)", waitErr, r1.err, r2.err)
	case r1.err != nil:
		return out, fmt.Errorf("stdout read error: %w", r1.err)
	case r2.err != nil:
		return out, fmt.Errorf("stderr read error: %w", r2.err)
	}
	return out, nil
}

// commandReferencesUploadFile scans a shell command for tokens
// that look like paths or filenames pointing into the chat
// upload directory. Returns the matched path if found, "" if
// the command is safe. The check is best-effort: it splits on
// common shell-tokenising characters and on whitespace, then
// asks isInUploadDir() for each token. The LLM doesn't try to
// obfuscate, so a simple split is enough.
func commandReferencesUploadFile(cmd string) string {
	if cmd == "" {
		return ""
	}
	// Split on whitespace and on common shell metachars.
	for _, sep := range []string{" ", "\t", "\n", ">", "<", "|", ";", "&", "(", ")", "\"", "'", "`", ","} {
		cmd = strings.ReplaceAll(cmd, sep, " ")
	}
	for _, tok := range strings.Fields(cmd) {
		if isInUploadDir(tok) {
			return tok
		}
	}
	return ""
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
	a.Path = resolveToProjectRoot(ctx, a.Path)

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
	a.Path = resolveToProjectRoot(ctx, a.Path)

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
	a.Path = resolveToProjectRoot(ctx, a.Path)

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
	// Expand leading ~ to user home so the absolute-path check
	// below catches ~/.p-chat/uploads/secret.png. Without this,
	// the LLM could read upload files via the shell escape hatch
	// by using the home-prefixed path (which would otherwise be
	// treated as a bare token, not an absolute path).
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				p = home
			} else if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
				p = filepath.Join(home, p[2:])
			}
		}
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
	a.Path = resolveToProjectRoot(ctx, a.Path)
	if isInUploadDir(a.Path) {
		return &CallResult{
			Content: fmt.Sprintf(
				"E_UPLOAD_DIR: read blocked — %s is inside the chat upload directory. "+
					"Uploaded files are already inlined in the user message; do NOT call "+
					"read_docx on them.", a.Path),
			IsError: true,
		}, nil
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
	a.Path = resolveToProjectRoot(ctx, a.Path)
	if isInUploadDir(a.Path) {
		return &CallResult{
			Content: fmt.Sprintf(
				"E_UPLOAD_DIR: read blocked — %s is inside the chat upload directory. "+
					"Uploaded files are already inlined in the user message; do NOT call "+
					"read_pdf on them.", a.Path),
			IsError: true,
		}, nil
	}
	text, err := readPdf(a.Path)
	if err != nil {
		return &CallResult{Content: err.Error(), IsError: true}, nil
	}
	return &CallResult{Content: text}, nil
}

type webFetchArgs struct {
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
	Body   string `json:"body,omitempty"`
}

func handleWebFetch(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a webFetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.URL == "" {
		return &CallResult{Content: "url is required", IsError: true}, nil
	}

	lower := strings.ToLower(a.URL)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return &CallResult{Content: "E_PROTO: only http:// and https:// URLs are supported", IsError: true}, nil
	}
	if strings.HasPrefix(lower, "http://127.") || strings.HasPrefix(lower, "http://localhost") || strings.HasPrefix(lower, "http://0.0.0.0") {
		return &CallResult{Content: "E_PROTO: fetching from loopback addresses is not allowed for security", IsError: true}, nil
	}

	method := "GET"
	if a.Method != "" {
		method = strings.ToUpper(a.Method)
	}
	if method != "GET" && method != "POST" {
		return &CallResult{Content: "E_ARGS: method must be GET or POST", IsError: true}, nil
	}

	var reqBody io.Reader
	if a.Body != "" {
		if method != "POST" {
			return &CallResult{Content: "E_ARGS: body is only valid with POST method", IsError: true}, nil
		}
		reqBody = strings.NewReader(a.Body)
	}

	httpCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, method, a.URL, reqBody)
	if err != nil {
		return &CallResult{Content: "request error: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "P-Chat/1.0")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &CallResult{Content: "fetch error: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &CallResult{Content: "read error: " + err.Error(), IsError: true}, nil
	}

	statusPrefix := ""
	if resp.StatusCode >= 400 {
		statusPrefix = fmt.Sprintf("HTTP %d\n\n", resp.StatusCode)
	}

	cType := strings.ToLower(resp.Header.Get("Content-Type"))
	// Detect binary content and refuse to return raw bytes to the
	// LLM (they poison the model's context and waste tokens).
	// Text-like content types: text/*, application/json, *+json,
	// application/xml, application/x-www-form-urlencoded,
	// application/javascript, empty.
	isBinaryCT := false
	if cType != "" && !strings.HasPrefix(cType, "text/") &&
		!strings.HasPrefix(cType, "application/json") &&
		!strings.HasPrefix(cType, "application/xml") &&
		!strings.HasPrefix(cType, "application/javascript") &&
		!strings.HasPrefix(cType, "application/x-www-form-urlencoded") &&
		!strings.HasSuffix(cType, "+json") && !strings.HasSuffix(cType, "+xml") {
		isBinaryCT = true
	}
	// Sniff first 512 bytes for NUL bytes (binary marker).
	if !isBinaryCT {
		end := 512
		if len(body) < end {
			end = len(body)
		}
		for _, b := range body[:end] {
			if b == 0 {
				isBinaryCT = true
				break
			}
		}
	}
	if isBinaryCT {
		return &CallResult{
			Content: fmt.Sprintf(
				"E_BINARY: response is binary content (Content-Type: %q, %d bytes); "+
					"web_fetch only returns text to the LLM. If you need the raw bytes, "+
					"download via exec_command (curl/Invoke-WebRequest) and write to disk.",
				resp.Header.Get("Content-Type"), len(body)),
			IsError: true,
		}, nil
	}

	return &CallResult{Content: statusPrefix + string(body)}, nil
}
