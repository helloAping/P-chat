package llm

// ChatMessage is the protocol-agnostic message format used for
// conversation persistence and internal agent processing. Each
// message represents one logical unit — a text prompt, an image
// attachment, a tool call, a thinking block, etc. — as a separate
// row in the conversation history.
//
// Protocol adapters (internal/llm/adapter.go) translate the
// [ChatMessage] list into the appropriate LLM wire format
// (OpenAI ChatCompletionRequest, Anthropic MessagesRequest, etc.),
// silently dropping parts that the protocol cannot represent
// (e.g. thinking blocks are never sent to the LLM).
type ChatMessage struct {
	// ── identity ──
	Role string `json:"role"` // system | user | assistant | tool

	// ── message kind ──
	Type string `json:"type,omitempty"` // text | image | audio | file | thinking | tool_call | tool_result

	// ── primary payload ──
	Content string `json:"content"` // text body, base64 raw data, JSON tool input, or tool result

	// ── attachment metadata (type = image / audio / file) ──
	Name     string `json:"name,omitempty"`      // original filename
	MimeType string `json:"mime_type,omitempty"` // image/png, audio/mp3, ...

	// ── tool metadata (type = tool_call / tool_result) ──
	ToolID    string `json:"tool_id,omitempty"`    // matches tool_use.id ↔ tool_result.tool_use_id
	ToolName  string `json:"tool_name,omitempty"`  // tool function name
	ToolInput string `json:"tool_input,omitempty"` // JSON arguments (tool_call), or empty for tool_result
	ToolError bool   `json:"tool_error,omitempty"` // true when tool_result.Content carries an error

	// ── extension ──
	Meta map[string]interface{} `json:"meta,omitempty"` // arbitrary extension data
}

// ── Role constants ──

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ── Type constants ──

const (
	TypeText       = "text"
	TypeImage      = "image"
	TypeAudio      = "audio"
	TypeFile       = "file"
	TypeThinking   = "thinking"
	TypeToolCall   = "tool_call"
	TypeToolResult = "tool_result"
)
