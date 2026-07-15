package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	transport Transport
	idSeq     atomic.Int64

	pending   map[int]chan JSONRPCResponse
	pendingMu sync.Mutex

	initialized bool
	done        chan struct{}
}

func NewClient(transport Transport) *Client {
	c := &Client{
		transport: transport,
		pending:   make(map[int]chan JSONRPCResponse),
		done:      make(chan struct{}),
	}
	c.idSeq.Store(1)
	return c
}

func (c *Client) Start(ctx context.Context) error {
	if err := c.transport.Start(ctx); err != nil {
		return fmt.Errorf("transport start: %w", err)
	}
	go c.runRecvLoop()
	return nil
}

func (c *Client) Initialize(ctx context.Context) error {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "initialize",
		Params: InitializeRequest{
			ProtocolVersion: protocolVersion,
			Capabilities:    map[string]any{},
			ClientInfo: ClientInfo{
				Name:    "pchat",
				Version: "1.0.0",
			},
		},
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize: server error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}

	notif := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		Method:  "notifications/initialized",
	}
	if err := c.transport.Send(notif); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	c.initialized = true
	return nil
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "tools/list",
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list: server error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	var result ListToolsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}
	return result.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*CallToolResult, error) {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "tools/call",
		Params: CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/call %s: %w", name, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/call %s: server error %d: %s", name, resp.Error.Code, resp.Error.Message)
	}

	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}
	return &result, nil
}

func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "resources/list",
	}
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resources/list: %w", err)
	}
	if resp.Error != nil {
		// Some servers don't support resources; return empty list
		return nil, nil
	}
	var result ListResourcesResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse resources/list: %w", err)
	}
	return result.Resources, nil
}

func (c *Client) ReadResource(ctx context.Context, uri string) (*ReadResourceResult, error) {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "resources/read",
		Params:  ReadResourceParams{URI: uri},
	}
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resources/read: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("resources/read: server error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var result ReadResourceResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse resources/read: %w", err)
	}
	return &result, nil
}

func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "prompts/list",
	}
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("prompts/list: %w", err)
	}
	if resp.Error != nil {
		return nil, nil
	}
	var result ListPromptsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse prompts/list: %w", err)
	}
	return result.Prompts, nil
}

func (c *Client) GetPrompt(ctx context.Context, name string, args map[string]any) (*GetPromptResult, error) {
	req := JSONRPCRequest{
		JSONRPC: jsonrpcVersion,
		ID:      int(c.idSeq.Add(1)),
		Method:  "prompts/get",
		Params:  GetPromptParams{Name: name, Arguments: args},
	}
	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("prompts/get: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("prompts/get: server error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var result GetPromptResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse prompts/get: %w", err)
	}
	return &result, nil
}

func (c *Client) Close() error {
	close(c.done)
	return c.transport.Close()
}

func (c *Client) sendRequest(ctx context.Context, req JSONRPCRequest) (*JSONRPCResponse, error) {
	respCh := make(chan JSONRPCResponse, 1)

	c.pendingMu.Lock()
	c.pending[req.ID] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, req.ID)
		c.pendingMu.Unlock()
	}()

	if err := c.transport.Send(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	select {
	case resp := <-respCh:
		if resp.ID != req.ID {
			return nil, fmt.Errorf("response id mismatch: expected %d, got %d", req.ID, resp.ID)
		}
		return &resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(120 * time.Second):
		return nil, fmt.Errorf("request timeout")
	}
}

func (c *Client) runRecvLoop() {
	for {
		select {
		case <-c.done:
			return
		case resp, ok := <-c.transport.Recv():
			if !ok {
				return
			}
			if resp.ID == 0 {
				continue
			}
			c.pendingMu.Lock()
			ch, ok := c.pending[resp.ID]
			c.pendingMu.Unlock()
			if ok {
				select {
				case ch <- resp:
				default:
				}
			}
		}
	}
}
