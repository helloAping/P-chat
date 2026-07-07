// Package httpcli is a thin HTTP client for pchat-server's REST API.
// Both the CLI REPL and the Wails GUI use it; the only process that
// actually touches memory/agent/tools is the server.
package httpcli

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

// Client talks to pchat-server over HTTP. All methods are safe for
// concurrent use.
type Client struct {
	base string
	http *http.Client

	// Cached "current" selection. Mirrors what the server thinks
	// the active session / provider / model is, so CLI helpers like
	// `/provider` don't have to round-trip for every command.
	mu              sync.Mutex
	currentProvider string
	currentStyle   string

	// Local cache of providers, populated by SetCfgProviders.
	// Used by SetModel / DisplayModel / ModelsFor which the server
	// doesn't expose as dedicated endpoints.
	cfgProviders []ProviderInfo
}

// NewClient builds a client targeting the given base URL (e.g.
// "http://127.0.0.1:8960"). No timeout is set on the HTTP client —
// SSE streams for long agent runs can last minutes; the caller
// controls cancellation via context.
func NewClient(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 0},
	}
}

// Session mirrors server.SessionResponse.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Message mirrors server.MessageResponse.
type Message struct {
	ID         int64  `json:"id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// StreamEvent mirrors server.StreamEvent (the SSE chunk format).
type StreamEvent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`

	// Phase/tool fields
	Phase       string `json:"phase,omitempty"`
	Step        string `json:"step,omitempty"`
	Msg         string `json:"message,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	ToolStatus  string `json:"tool_status,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	ToolError   string `json:"tool_error,omitempty"`
	ToolElapsed string `json:"tool_elapsed,omitempty"`

	// Done fields
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`

	// Error fields
	Error      string `json:"error,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`

	// Question event (LLM asks user a question)
	QuestionJSON string `json:"question_json,omitempty"`
}

// ProviderInfo mirrors the providers endpoint payload.
type ProviderInfo struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Protocol string `json:"protocol"`
}

// Model mirrors a single model entry returned by the server. We
// re-use it in two places: (1) the per-provider model list (future
// endpoint), (2) the current-model field on ProviderInfo. The JSON
// shape is intentionally small so it can sit in either payload.
type Model struct {
	Name             string `json:"name"`
	DisplayName      string `json:"display_name,omitempty"`
	Default          bool   `json:"default,omitempty"`
	Description      string `json:"description,omitempty"`
	MaxTokensContext int    `json:"max_tokens_context,omitempty"`
	MaxTokensOutput  int    `json:"max_tokens_output,omitempty"`
}

// StyleInfo mirrors the styles endpoint payload.
type StyleInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

// ====================================================================
// HTTP helpers
// ====================================================================

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode: %w (body: %s)", err, string(respBody))
		}
	}
	return nil
}

// Ping checks server reachability by calling /health. Returns nil
// on 200 OK.
func (c *Client) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/v1/health", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ====================================================================
// Session API
// ====================================================================

func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var resp struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/sessions", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, "GET", "/api/v1/sessions/"+id, nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

type CreateSessionOpts struct {
	Style    string `json:"style,omitempty"`
	Provider string `json:"provider,omitempty"`
	Title    string `json:"title,omitempty"`
}

func (c *Client) CreateSession(ctx context.Context, opts CreateSessionOpts) (*Session, error) {
	var s Session
	if err := c.doJSON(ctx, "POST", "/api/v1/sessions", opts, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) RenameSession(ctx context.Context, id, title string) error {
	body := struct {
		Title string `json:"title"`
	}{Title: title}
	return c.doJSON(ctx, "PATCH", "/api/v1/sessions/"+id, body, nil)
}

func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.doJSON(ctx, "DELETE", "/api/v1/sessions/"+id, nil, nil)
}

// ClearMessages empties the messages of a session without
// changing the session ID. Use this instead of DeleteSession
// when the user wants to start a fresh thread but keep the
// session row (so external refs stay stable).
func (c *Client) ClearMessages(ctx context.Context, id string) error {
	return c.doJSON(ctx, "DELETE", "/api/v1/sessions/"+id+"/messages", nil, nil)
}

func (c *Client) ListMessages(ctx context.Context, id string) ([]Message, error) {
	var resp struct {
		Messages []Message `json:"messages"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/sessions/"+id+"/messages", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
}

// SendMessageOptions configures a chat call.
type SendMessageOptions struct {
	Message string `json:"message" binding:"required"`
	Style   string `json:"style,omitempty"`
}

// SendMessage streams the LLM response back via SSE. Each event
// from the server is delivered to `onEvent`. The call returns when
// the server closes the stream (Done event or terminal error).
func (c *Client) SendMessage(ctx context.Context, sessionID string, opts SendMessageOptions, onEvent func(StreamEvent)) error {
	body, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.base+"/api/v1/sessions/"+sessionID+"/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	// Parse SSE: each event is "data: <json>\n\n"
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}
		var ev StreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// Skip malformed event but keep streaming.
			continue
		}
		onEvent(ev)
	}
	return scanner.Err()
}

// ====================================================================
// Metadata API
// ====================================================================

func (c *Client) ListProviders(ctx context.Context) ([]ProviderInfo, error) {
	var resp struct {
		Providers []ProviderInfo `json:"providers"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/providers", nil, &resp); err != nil {
		return nil, err
	}
	// Cache so SetModel / DisplayModel can answer without a
	// round-trip.
	c.cfgProviders = resp.Providers
	return resp.Providers, nil
}

func (c *Client) ListStyles(ctx context.Context) ([]StyleInfo, error) {
	var resp struct {
		Styles []StyleInfo `json:"styles"`
	}
	if err := c.doJSON(ctx, "GET", "/api/v1/styles", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Styles, nil
}

// ====================================================================
// Stubs the HTTP client is missing today; the HTTP REPL uses them.
// The server doesn't yet expose SetModel/DisplayModel/ProviderModel
// endpoints, so the client returns the locally-cached values it
// knows about (e.g. from the most recent /sessions response).
// ====================================================================

// currentProvider is the provider name the most recent operation
// was on. The client uses it to answer ProviderName() / DisplayModel()
// without round-tripping the server.
func (c *Client) CurrentProvider() string   { return c.currentProvider }
func (c *Client) SetCurrentProvider(p string) { c.currentProvider = p }

func (c *Client) ProviderModel() string {
	if c.currentProvider == "" {
		return ""
	}
	models, _ := c.ModelsFor(c.currentProvider)
	for _, m := range models {
		if m.Default {
			return m.Name
		}
	}
	if len(models) > 0 {
		return models[0].Name
	}
	return ""
}

// SetModel selects a model on a provider. Currently the server
// has no endpoint for this (the client must already have the model
// in its registry). Returns an error if the model is unknown so
// the REPL can fall back to a picker UI.
func (c *Client) SetModel(provider, model string) error {
	models, ok := c.ModelsFor(provider)
	if !ok {
		return fmt.Errorf("provider %q not found", provider)
	}
	for _, m := range models {
		if m.Name == model {
			c.currentProvider = provider
			return nil
		}
	}
	return fmt.Errorf("model %q not found in provider %q", model, provider)
}

func (c *Client) DisplayModel(provider string) string {
	models, ok := c.ModelsFor(provider)
	if !ok || len(models) == 0 {
		return ""
	}
	for _, m := range models {
		if m.Default && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	return models[0].DisplayName
}

// SubmitQuestionResponse sends the user's answer to a pending
// question back to the server so the agent can continue.
func (c *Client) SubmitQuestionResponse(ctx context.Context, sessionID string, answers map[string]string) error {
	body := map[string]interface{}{
		"answers": answers,
	}
	return c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/question-response", body, nil)
}

// Flush is a no-op for the HTTP client; the server persists data
// in its own SQLite store.
func (c *Client) Flush() error { return nil }

// ModelsFor is exposed for the legacy single-model form. Kept here
// to keep the public surface of the client package small.
func (c *Client) ModelsFor(provider string) ([]Model, bool) {
	// For now the client doesn't have a per-provider model endpoint;
	// synthesize a single-model entry from what we know.
	for _, p := range c.cfgProviders {
		if p.Name == provider {
			if p.Model != "" {
				return []Model{{Name: p.Model, Default: true}}, true
			}
			return nil, true
		}
	}
	return nil, false
}

// SetCfgProviders lets callers (typically the REPL on startup)
// inject the configured providers so the client can answer model
// lookups locally. The HTTP REPL reads this from the
// /api/v1/providers endpoint.
func (c *Client) SetCfgProviders(ps []ProviderInfo) {
	c.cfgProviders = ps
}

// Internal state.
func (c *Client) cfgProv() []ProviderInfo { return c.cfgProviders }

// ====================================================================
// Rollback API
// ====================================================================

// RollbackResult is the server response for a rollback request.
type RollbackResult struct {
	DeletedCount    int       `json:"deleted_count"`
	DeletedMessages []Message `json:"deleted_messages"`
}

// RollbackMessages deletes the message with beforeID and all later
// messages in the session. Returns the deleted messages for undo.
func (c *Client) RollbackMessages(ctx context.Context, sessionID string, beforeID int64) (*RollbackResult, error) {
	body := struct {
		BeforeID int64 `json:"before_id"`
	}{BeforeID: beforeID}
	var result RollbackResult
	if err := c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/rollback", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UndoRollbackMessages restores previously-deleted messages.
func (c *Client) UndoRollbackMessages(ctx context.Context, sessionID string, messages []Message) error {
	body := struct {
		Messages []Message `json:"messages"`
	}{Messages: messages}
	return c.doJSON(ctx, "POST", "/api/v1/sessions/"+sessionID+"/rollback/undo", body, nil)
}

// ForkSession creates a new session containing all messages up to and
// including beforeID from the source session.
func (c *Client) ForkSession(ctx context.Context, sessionID string, beforeID int64) (*Session, error) {
	body := map[string]int64{"before_id": beforeID}
	var s Session
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/sessions/"+sessionID+"/fork", body, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
