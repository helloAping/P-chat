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

	// ── new classification fields (v2 message model) ──
	// MsgType is the numeric content-type enum (0=text 1=image
	// 2=video 3=audio 4=tool 5=command 6=question). Replaces
	// string-based Type for filtering and rendering dispatch.
	MsgType int `json:"msg_type,omitempty"`
	// SubmitToLLM controls whether this message is included when
	// building the LLM conversation context. 0=display-only
	// (system prompts, thinking, raw command output), 1=context.
	SubmitToLLM int `json:"submit_to_llm,omitempty"`

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

// ── Type constants (legacy string-based, maintained for backward compat) ──

const (
	TypeText       = "text"
	TypeImage      = "image"
	TypeAudio      = "audio"
	TypeVideo      = "video"
	TypeFile       = "file"
	TypeThinking   = "thinking"
	TypeToolCall   = "tool_call"
	TypeToolResult = "tool_result"
)

// ── MsgType constants (numeric, v2 message model) ──

const (
	MsgTypeText     = 0 // text (user / assistant / system prose)
	MsgTypeImage    = 1 // image attachment
	MsgTypeVideo    = 2 // video attachment
	MsgTypeAudio    = 3 // audio attachment
	MsgTypeTool     = 4 // tool call / tool result (LLM context only, not rendered)
	MsgTypeCommand  = 5 // exec_command raw output (display-only, terminal panel)
	MsgTypeQuestion = 6 // LLM question + user answer (table card)
)

// MsgTypeForLegacy maps a legacy string Type to the numeric MsgType.
// Returns msg_type and submit_to_llm defaults.
func MsgTypeForLegacy(t string, toolName string) (msgType int, submitToLLM int) {
	switch t {
	case TypeText:
		return MsgTypeText, 1
	case TypeImage:
		return MsgTypeImage, 1
	case TypeAudio:
		return MsgTypeAudio, 1
	case TypeVideo:
		return MsgTypeVideo, 1
	case TypeToolCall:
		return MsgTypeTool, 1
	case TypeToolResult:
		if toolName == "exec_command" {
			return MsgTypeCommand, 0
		}
		return MsgTypeTool, 1
	case TypeThinking:
		return MsgTypeText, 0
	default:
		return MsgTypeText, 1
	}
}
