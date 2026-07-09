package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/llm"
)

// TestRollbackMessages_ReturnsDecodedParts locks in the wire
// shape of the rollback response: `deleted_messages` must come
// out as MessageResponse (with `parts` decoded from metadata),
// NOT as raw memory.Message (with only `metadata` as a string).
//
// Without this, the frontend's `undoRollback` splices the
// messages back into its in-memory array but each item has
// `parts: undefined`. MessageBubble.vue's parts-driven render
// path then falls back to plain-text rendering of `content`,
// silently dropping the thinking block, tool call cards,
// sub-agent cards, and question cards that the user had
// before rolling back. Visually the messages come back, but
// the structural formatting is gone — the bug reported in
// 2026-07-09.
//
// The test creates an assistant message whose `parts` blob
// includes a tool call + a sub-agent, then rolls back to
// before that message, and asserts the deleted_messages
// response carries the decoded parts (not just raw metadata).
func TestRollbackMessages_ReturnsDecodedParts(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store

	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// User message (the rollback anchor) — will be the
	// `before_id` boundary.
	store.AddMessage(llm.Message{Role: "user", Content: "hi"})
	// Assistant message with a rich parts blob. The blob
	// matches what the live agent's partsAcc actually
	// writes: text + thinking + tool + sub-agent, all
	// interleaved in stream order. decodePartsFromMeta
	// returns this blob verbatim when it contains at
	// least one text or thinking part (which is the v2
	// full-snapshot format), so the test sees the same
	// shape the user saw during live streaming.
	partsBlob := []agent.MessagePart{
		{Kind: "text", Text: "hello there"},
		{Kind: "thinking", Text: "let me think"},
		{Kind: "tool", Name: "read_file", Args: `{"path":"x"}`, Status: "ok", Result: "data", Elapsed: "5ms"},
		{Kind: "sub_agent", Task: "list repo", Status: "ok", Elapsed: "1s", Parts: []agent.MessagePart{
			{Kind: "text", Text: "found 3 files"},
		}},
	}
	partsJSON, _ := json.Marshal(partsBlob)
	store.AddMessageWithMeta(llm.Message{Role: "assistant", Content: "hello there"}, map[string]string{
		"thinking": "let me think",
		"parts":    string(partsJSON),
	})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Rollback to before the assistant message. The
	// frontend would call this with the assistant's id
	// (or the user's id, depending on UX — for the test
	// we just need any before_id that's >= the assistant's
	// id to capture both).
	// Use the assistant message's id so the rollback
	// captures only the assistant row.
	_, _, _, rowIDs := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(rowIDs) < 2 {
		t.Fatalf("setup: want >= 2 rows, got %d", len(rowIDs))
	}
	beforeID := rowIDs[1]

	body, _ := json.Marshal(map[string]any{"before_id": beforeID})
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/rollback",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		DeletedCount   int               `json:"deleted_count"`
		DeletedMsgs    []MessageResponse `json:"deleted_messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DeletedCount != 1 || len(resp.DeletedMsgs) != 1 {
		t.Fatalf("want 1 deleted message, got %d (count) / %d (msgs)", resp.DeletedCount, len(resp.DeletedMsgs))
	}

	// The crucial assertion: `parts` must be populated
	// (decoded from metadata). Pre-fix this was empty
	// because the handler returned raw memory.Message.
	got := resp.DeletedMsgs[0]
	if got.Role != "assistant" {
		t.Errorf("role = %q, want assistant", got.Role)
	}
	if len(got.Parts) == 0 {
		t.Fatalf("deleted_messages[0].parts is EMPTY — the bug is back. Pre-fix this was the case; the parts-driven render path would have fallen back to plain text.")
	}

	// We expect: text, thinking, tool, sub_agent (in
	// stream order — that's the order they appear in the
	// agent's partsAcc snapshot, which the agent writes
	// verbatim to meta["parts"] during streaming).
	wantOrder := []string{"text", "thinking", "tool", "sub_agent"}
	if len(got.Parts) != len(wantOrder) {
		t.Fatalf("parts count = %d, want %d (%+v)", len(got.Parts), len(wantOrder), got.Parts)
	}
	for i, want := range wantOrder {
		if got.Parts[i].Kind != want {
			t.Errorf("parts[%d].Kind = %q, want %q", i, got.Parts[i].Kind, want)
		}
	}
	// Spot-check the tool card made it through with all
	// its data (status, result, etc.).
	tool := got.Parts[2]
	if tool.Name != "read_file" || tool.Status != "ok" || tool.Result != "data" {
		t.Errorf("tool part lost detail: %+v", tool)
	}
	// And the sub-agent kept its nested parts.
	sub := got.Parts[3]
	if sub.Task != "list repo" || len(sub.Parts) != 1 || sub.Parts[0].Text != "found 3 files" {
		t.Errorf("sub-agent lost detail: %+v", sub)
	}
}

// TestRollbackMessages_FiltersStandaloneToolRows verifies
// the standard buildMessageResponse filter (tool_call /
// tool_result / exec_command rows return nil) still applies
// in the rollback path. The standalone rows' data is
// already embedded in the parent assistant message's parts,
// so returning them as separate items would create duplicates
// in the chat. `deleted_count` reflects the *filtered* count
// so the frontend's `splice(fromIndex, 0, ...count)` lines up
// with what it originally removed via `splice(messageIndex)`.
func TestRollbackMessages_FiltersStandaloneToolRows(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store

	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// User + assistant (with tool part in parts, plus
	// a text part so decodePartsFromMeta takes the v2
	// full-snapshot path) + standalone tool_call row +
	// tool_result row. This matches the real DB shape —
	// the agent's persistAssistant embeds the tool call
	// data into the assistant's parts, but the per-call
	// tool_call and tool_result rows also exist for
	// LLM-context reconstruction.
	store.AddMessage(llm.Message{Role: "user", Content: "hi"})
	partsBlob := []agent.MessagePart{
		{Kind: "text", Text: "hi back"},
		{Kind: "tool", ToolID: "call_1", Name: "read_file", Args: `{"path":"x"}`, Status: "ok", Result: "data"},
	}
	partsJSON, _ := json.Marshal(partsBlob)
	store.AddMessageWithMeta(llm.Message{Role: "assistant", Content: "hi back"}, map[string]string{
		"parts": string(partsJSON),
	})
	// Standalone tool_call row (what AddChatMessageTo would
	// write for a native tool call). msg_type=4 + type=tool_call
	// in metadata; buildMessageResponse filters it. We use
	// AddChatMessageTo (not AddMessage) so the msg_type
	// column is set correctly — AddMessage takes the
	// OpenAI-style llm.Message which has no MsgType field.
	store.AddChatMessageTo(store.CurrentConversationID(), llm.ChatMessage{
		Role:    llm.RoleAssistant,
		Type:    llm.TypeToolCall,
		ToolID:  "call_1",
		ToolName: "read_file",
		ToolInput: `{"path":"x"}`,
		MsgType: llm.MsgTypeTool,
	})
	// Standalone tool_result row. role=tool (so the LLM
	// can see it on the next round) + msg_type=4 +
	// type=tool_result in metadata.
	store.AddChatMessageTo(store.CurrentConversationID(), llm.ChatMessage{
		Role:    llm.RoleTool,
		Type:    llm.TypeToolResult,
		Content: "data",
		ToolID:  "call_1",
		ToolName: "read_file",
		MsgType: llm.MsgTypeTool,
	})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Rollback to the second message's id (the user msg) —
	// that captures the assistant + the two standalone rows.
	_, _, _, rowIDs := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	beforeID := rowIDs[1]

	body, _ := json.Marshal(map[string]any{"before_id": beforeID})
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/rollback",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		DeletedCount int               `json:"deleted_count"`
		DeletedMsgs  []MessageResponse `json:"deleted_messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Raw DB has 3 rows from the user msg onwards
	// (1 assistant with text+tool, 1 standalone tool_call,
	// 1 standalone tool_result). The filter drops the 2
	// standalone tool rows → 1 returned (the assistant).
	if resp.DeletedCount != 1 {
		t.Errorf("deleted_count = %d, want 1 (3 raw rows, 2 tool rows filtered)", resp.DeletedCount)
	}
	if len(resp.DeletedMsgs) != 1 {
		t.Fatalf("len(deleted_messages) = %d, want 1", len(resp.DeletedMsgs))
	}
	// Surviving role: assistant.
	asst := resp.DeletedMsgs[0]
	if asst.Role != "assistant" {
		t.Errorf("role = %q, want assistant", asst.Role)
	}
	// The assistant message must still carry its parts
	// (text + tool), since the standalone tool_call row
	// was filtered but the data was already in the
	// parts snapshot.
	if len(asst.Parts) != 2 {
		t.Fatalf("parts count = %d, want 2 (text + tool): %+v", len(asst.Parts), asst.Parts)
	}
	if asst.Parts[0].Kind != "text" || asst.Parts[0].Text != "hi back" {
		t.Errorf("parts[0] = %+v, want text 'hi back'", asst.Parts[0])
	}
	if asst.Parts[1].Kind != "tool" || asst.Parts[1].Name != "read_file" ||
		asst.Parts[1].Result != "data" {
		t.Errorf("parts[1] = %+v, want tool read_file with result 'data'", asst.Parts[1])
	}
}
