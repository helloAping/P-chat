package llm

import (
	"encoding/json"
	"io"
	"net/http"
)

// ToolDef is a protocol-agnostic tool definition. Each registered
// tool is described by its name, human-readable description, and
// a JSON Schema for its parameters. Adapters translate these into
// the appropriate wire format (OpenAI Tool, Anthropic tool, etc.).
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema object for the tool's parameters
}

// ProtocolRequest is the output of an adapter's Build method. It
// contains everything needed to send the HTTP request: method,
// full URL, JSON body, and headers.
type ProtocolRequest struct {
	Method  string
	URL     string
	Body    []byte
	Headers map[string]string
}

// ProtocolAdapter converts protocol-agnostic ChatMessage lists
// into the LLM's native wire format (OpenAI or Anthropic) and
// parses the streaming response back into a uniform StreamChunk
// channel.
//
// Adapters silently skip message types that their protocol cannot
// represent. For example, thinking blocks and sub-agent internal
// messages are agent-internal only and are never sent to the
// model.
type ProtocolAdapter interface {
	// Build converts a list of ChatMessage + tool definitions into
	// a protocol-specific HTTP request. The system prompt is
	// passed separately because Anthropic requires it on the
	// top-level `system` field rather than as a message.
	Build(messages []ChatMessage, model string, maxTokens int, tools []ToolDef, system string, temperature float32, topP float32) (*ProtocolRequest, error)

	// ParseStream reads the protocol's SSE/stream response body
	// and emits StreamChunk values on the returned channel.
	// Parsing runs in a goroutine; the channel is closed when
	// the stream ends or an error occurs.
	ParseStream(r io.Reader) <-chan StreamChunk
}

// NewHTTPClient is a test seam: adapters call this to get an HTTP
// client. Production code uses http.DefaultClient; tests can
// override it with a round-tripper.
var NewHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 0}
}
