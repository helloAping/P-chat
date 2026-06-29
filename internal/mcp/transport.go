package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

type Transport interface {
	Start(ctx context.Context) error
	Send(req JSONRPCRequest) error
	Recv() <-chan JSONRPCResponse
	Close() error
}

type StdioTransport struct {
	command string
	args    []string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	outCh   chan JSONRPCResponse
	cancel  context.CancelFunc
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

func NewStdioTransport(command string, args []string) *StdioTransport {
	return &StdioTransport{
		command: command,
		args:    args,
		outCh:   make(chan JSONRPCResponse, 64),
		done:    make(chan struct{}),
	}
}

func (t *StdioTransport) Start(ctx context.Context) error {
	t.cmd = exec.CommandContext(ctx, t.command, t.args...)

	var err error
	t.stdin, err = t.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	t.stdout, err = t.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	ctx, t.cancel = context.WithCancel(ctx)
	go t.readLoop(ctx)

	return nil
}

func (t *StdioTransport) Send(req JSONRPCRequest) error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return fmt.Errorf("transport closed")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')
	_, err = t.stdin.Write(data)
	return err
}

func (t *StdioTransport) Recv() <-chan JSONRPCResponse {
	return t.outCh
}

func (t *StdioTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	<-t.done
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Process.Kill()
		t.cmd.Wait()
	}
	return nil
}

func (t *StdioTransport) readLoop(ctx context.Context) {
	defer close(t.done)
	defer close(t.outCh)

	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		select {
		case t.outCh <- resp:
		case <-ctx.Done():
			return
		}
	}
}
