package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/tool"
)

func TestManagerStdioKeepsConnectionAndPassesEnv(t *testing.T) {
	reg := tool.NewRegistry()
	mgr := NewManager(reg)
	if err := mgr.AddServer(ServerConfig{
		Name:    "test_server",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess", "--"},
		Env: map[string]string{
			"PCHAT_MCP_HELPER":     "1",
			"PCHAT_MCP_TEST_VALUE": "from-env",
		},
		Enabled: true,
		Timeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop("test_server") })

	waitForMCPState(t, mgr, "test_server", StateRunning)

	// The previous implementation canceled the process as soon as
	// connectServer returned. Give that failure mode time to show up.
	time.Sleep(200 * time.Millisecond)

	fullName := mcpToolName("test_server", "echo_env_value")
	if _, _, ok := reg.Lookup(fullName); !ok {
		t.Fatalf("registered MCP tool %q not found", fullName)
	}
	got, err := mgr.CallTool(context.Background(), fullName, map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "from-env" {
		t.Fatalf("CallTool content = %+v, want from-env", got.Content)
	}
}

func TestMCPToolNameIsSafeAndCollisionResistant(t *testing.T) {
	a := mcpToolName("server_with_underscores", "tool_with_under_score")
	b := mcpToolName("server_with", "underscores_tool_with_under_score")
	if a == b {
		t.Fatalf("different server/tool pairs produced same name %q", a)
	}
	for _, name := range []string{a, b} {
		if len(name) > 64 {
			t.Fatalf("tool name too long: %d %q", len(name), name)
		}
		if !strings.HasPrefix(name, "mcp__") {
			t.Fatalf("tool name %q missing mcp prefix", name)
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			ok := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if !ok {
				t.Fatalf("tool name %q contains unsafe byte %q", name, c)
			}
		}
	}
}

func waitForMCPState(t *testing.T, mgr *Manager, name string, want ServerState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, info := range mgr.List() {
			if info.Name == name && info.State == want {
				return
			}
			if info.Name == name && info.State == StateError {
				t.Fatalf("server entered error state: %s", info.Error)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server %q did not reach state %q; got %+v", name, want, mgr.List())
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("PCHAT_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req JSONRPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = InitializeResult{
				ProtocolVersion: protocolVersion,
				Capabilities:    map[string]any{},
				ServerInfo:      MCPServerInfo{Name: "helper", Version: "test"},
			}
		case "tools/list":
			result = ListToolsResult{Tools: []Tool{{
				Name:        "echo_env_value",
				Description: "echoes the test env value",
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			}}}
		case "tools/call":
			result = CallToolResult{Content: []ContentItem{{
				Type: "text",
				Text: os.Getenv("PCHAT_MCP_TEST_VALUE"),
			}}}
		default:
			writeHelperResponse(req.ID, nil, &JSONRPCError{Code: -32601, Message: "method not found"})
			continue
		}
		writeHelperResponse(req.ID, result, nil)
	}
	os.Exit(0)
}

func writeHelperResponse(id int, result any, rpcErr *JSONRPCError) {
	var raw json.RawMessage
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	resp := JSONRPCResponse{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Result:  raw,
		Error:   rpcErr,
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(data))
}
