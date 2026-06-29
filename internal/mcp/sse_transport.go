package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type SSETransport struct {
	baseURL string

	httpClient *http.Client
	messageURL string

	outCh  chan JSONRPCResponse
	cancel context.CancelFunc
	done   chan struct{}

	closeMu sync.Mutex
	closed  bool
}

func NewSSETransport(baseURL string) *SSETransport {
	return &SSETransport{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 0},
		outCh:      make(chan JSONRPCResponse, 64),
		done:       make(chan struct{}),
	}
}

func (t *SSETransport) Start(ctx context.Context) error {
	ctx, t.cancel = context.WithCancel(ctx)

	sseURL := t.baseURL + "/sse"
	req, err := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	if err != nil {
		return fmt.Errorf("build SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return fmt.Errorf("SSE connect: HTTP %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)

	// Read the first SSE event to discover the message endpoint.
	// MCP spec: the server MUST send an "endpoint" event as its
	// first event, containing the relative or absolute URL for
	// POST /message.
	var eventType, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			resp.Body.Close()
			return fmt.Errorf("read endpoint event: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Blank line = end of event. Check if we have enough.
			if eventType == "endpoint" && data != "" {
				break
			}
			eventType = ""
			data = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(line[5:])
		}
	}

	messagePath := data
	if !strings.HasPrefix(messagePath, "http") {
		messagePath = t.baseURL + messagePath
	}
	t.messageURL = messagePath

	go t.readLoop(ctx, reader, resp.Body)
	return nil
}

func (t *SSETransport) Send(req JSONRPCRequest) error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return fmt.Errorf("transport closed")
	}
	if t.messageURL == "" {
		return fmt.Errorf("not started")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", t.messageURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build POST: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("POST message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST message: HTTP %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

func (t *SSETransport) Recv() <-chan JSONRPCResponse {
	return t.outCh
}

func (t *SSETransport) Close() error {
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
	return nil
}

func (t *SSETransport) readLoop(ctx context.Context, reader *bufio.Reader, body io.ReadCloser) {
	defer close(t.done)
	defer close(t.outCh)
	defer body.Close()

	var data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if data != "" {
				var resp JSONRPCResponse
				if json.Unmarshal([]byte(data), &resp) == nil && resp.ID != 0 {
					select {
					case t.outCh <- resp:
					case <-ctx.Done():
						return
					}
				}
			}
			data = ""
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(line[5:])
		}
	}
}
