package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const processBufferLimit = 256 * 1024

type managedProcess struct {
	id        string
	command   string
	workDir   string
	startedAt time.Time
	cmd       *exec.Cmd

	mu       sync.Mutex
	output   []byte
	exited   bool
	exitText string
}

type processSnapshot struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	WorkDir   string `json:"work_dir,omitempty"`
	StartedAt string `json:"started_at"`
	Running   bool   `json:"running"`
	ExitText  string `json:"exit_text,omitempty"`
	Output    string `json:"output,omitempty"`
}

var (
	processMu sync.Mutex
	processes = map[string]*managedProcess{}
)

type startProcessArgs struct {
	Command string `json:"command"`
	WorkDir string `json:"work_dir,omitempty"`
}

type processIDArgs struct {
	ProcessID string `json:"process_id"`
	MaxBytes  int    `json:"max_bytes,omitempty"`
}

func handleStartProcess(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a startProcessArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.Command == "" {
		return &CallResult{Content: "command is required", IsError: true}, nil
	}
	if sb := sandboxFromCtx(ctx); sb != nil && !sb.CheckExecBool(a.Command) {
		return &CallResult{Content: fmt.Sprintf("E_SANDBOX: command blocked by sandbox policy\n  command: %s", a.Command), IsError: true}, nil
	}
	if reason := commandReferencesUploadFile(a.Command); reason != "" {
		return &CallResult{Content: fmt.Sprintf("E_UPLOAD_DIR: command blocked - %s is inside the chat upload directory", reason), IsError: true}, nil
	}
	return startManagedProcess(ctx, a.Command, a.WorkDir)
}

func handleReadProcessOutput(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a processIDArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.ProcessID == "" {
		return &CallResult{Content: "process_id is required", IsError: true}, nil
	}
	p := getManagedProcess(a.ProcessID)
	if p == nil {
		return &CallResult{Content: "process not found: " + a.ProcessID, IsError: true}, nil
	}
	maxBytes := a.MaxBytes
	if maxBytes <= 0 || maxBytes > processBufferLimit {
		maxBytes = 32 * 1024
	}
	snap := p.snapshot(maxBytes)
	data, _ := json.MarshalIndent(snap, "", "  ")
	return &CallResult{Content: string(data)}, nil
}

func handleStopProcess(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var a processIDArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	if a.ProcessID == "" {
		return &CallResult{Content: "process_id is required", IsError: true}, nil
	}
	p := getManagedProcess(a.ProcessID)
	if p == nil {
		return &CallResult{Content: "process not found: " + a.ProcessID, IsError: true}, nil
	}
	if err := stopProcessTree(p.cmd); err != nil {
		return &CallResult{Content: "stop failed: " + err.Error(), IsError: true}, nil
	}
	time.Sleep(200 * time.Millisecond)
	snap := p.snapshot(16 * 1024)
	data, _ := json.MarshalIndent(snap, "", "  ")
	return &CallResult{Content: string(data)}, nil
}

func handleListProcesses(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	processMu.Lock()
	list := make([]*managedProcess, 0, len(processes))
	for _, p := range processes {
		list = append(list, p)
	}
	processMu.Unlock()

	snaps := make([]processSnapshot, 0, len(list))
	for _, p := range list {
		snaps = append(snaps, p.snapshot(0))
	}
	data, _ := json.MarshalIndent(snaps, "", "  ")
	return &CallResult{Content: string(data)}, nil
}

func startManagedProcess(ctx context.Context, command, workDir string) (*CallResult, error) {
	cmd, resolvedWorkDir := buildExecCommand(nil, command, workDir, projectRootFromCtx(ctx))
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &CallResult{Content: "stdout pipe failed: " + err.Error(), IsError: true}, nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return &CallResult{Content: "stderr pipe failed: " + err.Error(), IsError: true}, nil
	}
	if err := cmd.Start(); err != nil {
		return &CallResult{Content: "start failed: " + err.Error(), IsError: true}, nil
	}

	p := &managedProcess{
		id:        "proc_" + uuid.NewString(),
		command:   command,
		workDir:   resolvedWorkDir,
		startedAt: time.Now(),
		cmd:       cmd,
	}
	processMu.Lock()
	processes[p.id] = p
	processMu.Unlock()

	go p.capture(stdout)
	go p.capture(stderr)
	go p.wait()

	time.Sleep(250 * time.Millisecond)
	snap := p.snapshot(16 * 1024)
	data, _ := json.MarshalIndent(snap, "", "  ")
	return &CallResult{Content: string(data)}, nil
}

func getManagedProcess(id string) *managedProcess {
	processMu.Lock()
	defer processMu.Unlock()
	return processes[id]
}

func (p *managedProcess) capture(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.appendOutput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (p *managedProcess) appendOutput(chunk []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.output = append(p.output, chunk...)
	if len(p.output) > processBufferLimit {
		p.output = p.output[len(p.output)-processBufferLimit:]
	}
}

func (p *managedProcess) wait() {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.exited = true
	if err != nil {
		p.exitText = err.Error()
	} else {
		p.exitText = "exit 0"
	}
	p.mu.Unlock()
}

func (p *managedProcess) snapshot(maxBytes int) processSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.output
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	return processSnapshot{
		ID:        p.id,
		Command:   p.command,
		WorkDir:   p.workDir,
		StartedAt: p.startedAt.Format(time.RFC3339),
		Running:   !p.exited,
		ExitText:  p.exitText,
		Output:    string(bytes.TrimRight(out, "\x00")),
	}
}

func looksPersistentCommand(command string) bool {
	cmd := strings.ToLower(compactSpaces(command))
	if cmd == "" {
		return false
	}
	quickExit := []string{" -v", " --version", " -h", " --help", " -e "}
	for _, marker := range quickExit {
		if strings.Contains(cmd, marker) {
			return false
		}
	}
	markers := []string{
		"npm run dev", "npm start", "pnpm dev", "pnpm start", "yarn dev", "yarn start",
		"npm run serve", "pnpm serve", "yarn serve", "vite", "next dev", "nuxt dev",
		"webpack serve", "ts-node-dev", "nodemon", "python -m http.server", "http-server",
		"air", "watch", "--watch",
	}
	for _, marker := range markers {
		if strings.Contains(cmd, marker) {
			return true
		}
	}
	return strings.HasPrefix(cmd, "node ") || strings.HasPrefix(cmd, "java ")
}
