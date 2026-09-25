package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessagePartSubAgentRoundTrip locks in the wire format
// contract: a MessagePart with sub-agent metadata, when
// JSON-marshaled for the client, emits camelCase keys
// (agentType, agentColor, agentModel, taskId, runMode,
// agentDescription) — matching the frontend's TypeScript
// MessagePart type (src/api/client.ts:121-132). Without the
// custom MarshalJSON, the snake_case storage tags
// (agent_type, etc.) would surface on the wire and the
// frontend would silently lose the SubAgentCard's header
// label / accent color / model chip / task_id badge /
// description tooltip on session reload.
//
// Also verifies the reverse: storage JSON (snake_case
// keys, what the agent's `snapshotStructural` writes to
// meta["parts"]) unmarshals into the struct via the
// default decoder. This is the reload path: meta["parts"]
// is read, unmarshaled, then re-marshaled for the client.
func TestMessagePartSubAgentRoundTrip(t *testing.T) {
	// Storage shape: snake_case (matches the agent's
	// MessagePart in internal/agent/parts.go).
	storageJSON := `{
		"kind": "sub_agent",
		"task": "audit",
		"status": "ok",
		"agent_type": "explore",
		"agent_color": "#44BA81",
		"agent_model": "gpt-4o-mini",
		"task_id": "audit-2025-01-15",
		"run_mode": "async",
		"agent_description": "Fast read-only file search.",
		"elapsed": "12.3s"
	}`

	var p MessagePart
	if err := json.Unmarshal([]byte(storageJSON), &p); err != nil {
		t.Fatalf("unmarshal storage: %v", err)
	}

	// All fields populated via snake_case storage tags.
	if p.AgentType != "explore" {
		t.Errorf("AgentType = %q, want explore", p.AgentType)
	}
	if p.AgentColor != "#44BA81" {
		t.Errorf("AgentColor = %q, want #44BA81", p.AgentColor)
	}
	if p.AgentModel != "gpt-4o-mini" {
		t.Errorf("AgentModel = %q, want gpt-4o-mini", p.AgentModel)
	}
	if p.TaskID != "audit-2025-01-15" {
		t.Errorf("TaskID = %q, want audit-2025-01-15", p.TaskID)
	}
	if p.RunMode != "async" {
		t.Errorf("RunMode = %q, want async", p.RunMode)
	}
	if p.AgentDescription != "Fast read-only file search." {
		t.Errorf("AgentDescription = %q", p.AgentDescription)
	}
	if p.Task != "audit" {
		t.Errorf("Task = %q, want audit", p.Task)
	}

	// Re-marshal — custom MarshalJSON should produce the
	// camelCase wire format. We don't compare the full
	// string (it would drift on field ordering) — instead
	// we check the camelCase keys are present and the
	// snake_case keys are absent.
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(out)

	mustContain := []string{
		`"agentType":"explore"`,
		`"agentColor":"#44BA81"`,
		`"agentModel":"gpt-4o-mini"`,
		`"taskId":"audit-2025-01-15"`,
		`"runMode":"async"`,
		`"agentDescription":"Fast read-only file search."`,
	}
	for _, want := range mustContain {
		if !strings.Contains(wire, want) {
			t.Errorf("wire JSON missing %q\n  wire: %s", want, wire)
		}
	}
	mustNotContain := []string{
		`"agent_type"`, `"agent_color"`, `"agent_model"`,
		`"task_id"`, `"run_mode"`, `"agent_description"`,
	}
	for _, banned := range mustNotContain {
		if strings.Contains(wire, banned) {
			t.Errorf("wire JSON leaked storage key %q (should be camelCase only)\n  wire: %s", banned, wire)
		}
	}
}

// TestMessagePartQuestionStatusRoundTrip locks in the
// question_status wire format. Unlike the sub-agent fields,
// the frontend's TypeScript MessagePart type uses snake_case
// for this field (client.ts:143), so the server's snake_case
// JSON tag is the correct wire format. No custom marshal
// translation needed — the default behavior is right.
func TestMessagePartQuestionStatusRoundTrip(t *testing.T) {
	storageJSON := `{
		"kind": "question",
		"text": "{\"questions\":[{\"header\":\"心情\",\"question\":\"今天心情如何？\"}]}",
		"name": "{\"心情\":\"还行\"}",
		"question_status": "ok"
	}`
	var p MessagePart
	if err := json.Unmarshal([]byte(storageJSON), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.QuestionStatus != "ok" {
		t.Errorf("QuestionStatus = %q, want ok", p.QuestionStatus)
	}
	if p.Name != `{"心情":"还行"}` {
		t.Errorf("Name = %q", p.Name)
	}

	// Re-marshal: question_status should stay snake_case.
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(out)
	if !strings.Contains(wire, `"question_status":"ok"`) {
		t.Errorf("wire JSON missing question_status:ok\n  wire: %s", wire)
	}
}

// TestMessagePartEmptyFieldsOmitted verifies the wire
// format doesn't emit empty sub-agent metadata as
// `"agentType":""` — `omitempty` should drop them. Otherwise
// the SubAgentCard would render with `part.agentType = ""`,
// which the `v-if="part.agentType"` check would treat as
// "show the badge" because empty string is falsy in JS but
// the empty JSON key still leaks over the wire.
func TestMessagePartEmptyFieldsOmitted(t *testing.T) {
	p := MessagePart{Kind: "sub_agent", Task: "audit"}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wire := string(out)
	for _, banned := range []string{
		`"agentType"`, `"agentColor"`, `"agentModel"`,
		`"taskId"`, `"runMode"`, `"agentDescription"`, `"question_status"`,
		`"agent_type"`, `"agent_color"`, `"agent_model"`,
		`"task_id"`, `"run_mode"`, `"agent_description"`,
	} {
		if strings.Contains(wire, banned) {
			t.Errorf("empty metadata leaked: wire contains %q\n  wire: %s", banned, wire)
		}
	}
	// Sanity: the kind and task did make it through.
	if !strings.Contains(wire, `"kind":"sub_agent"`) {
		t.Errorf("wire missing kind\n  wire: %s", wire)
	}
	if !strings.Contains(wire, `"task":"audit"`) {
		t.Errorf("wire missing task\n  wire: %s", wire)
	}
}

func TestDecodePartsFromMetaTreatsNestedSubAgentTextAsFullSnapshot(t *testing.T) {
	parts := []MessagePart{
		{
			Kind:    "sub_agent",
			Task:    "background work",
			Status:  "ok",
			RunMode: "async",
			Parts: []MessagePart{
				{Kind: "tool", Name: "read_file", Status: "ok"},
				{Kind: "text", Text: "async result"},
			},
		},
	}
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	metaJSON, err := json.Marshal(map[string]string{"parts": string(partsJSON)})
	if err != nil {
		t.Fatal(err)
	}

	got := decodePartsFromMeta(string(metaJSON), "legacy plain content should not render")
	if len(got) != 1 {
		t.Fatalf("parts len = %d, want only sub_agent card: %+v", len(got), got)
	}
	if got[0].Kind != "sub_agent" || got[0].RunMode != "async" {
		t.Fatalf("sub_agent part not preserved: %+v", got[0])
	}
	if len(got[0].Parts) != 2 || got[0].Parts[1].Text != "async result" {
		t.Fatalf("nested sub_agent parts not preserved: %+v", got[0].Parts)
	}
}

// TestDecodePartsFromMetaScrubsStreamingFlags is the regression test for
// the 2026-09-25 "ghost thinking" bug: a turn interrupted before its Done
// chunk (server restart, deadline, kill) persists a parts snapshot with
// thinking parts still marked streaming=true. Replayed as-is, the UI
// showed a permanent "思考中…" on an idle session. History must never
// render as still streaming, so decodePartsFromMeta scrubs the flag on
// every thinking part, including nested sub-agent parts.
func TestDecodePartsFromMetaScrubsStreamingFlags(t *testing.T) {
	parts := []MessagePart{
		{Kind: "thinking", Text: "interrupted mid-reasoning", Streaming: true},
		{Kind: "text", Text: "partial answer"},
		{
			Kind:   "sub_agent",
			Task:   "background work",
			Status: "start",
			Parts: []MessagePart{
				{Kind: "thinking", Text: "nested still thinking", Streaming: true},
				{Kind: "tool", Name: "read_file", Status: "start"},
			},
		},
	}
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	metaJSON, err := json.Marshal(map[string]string{"parts": string(partsJSON)})
	if err != nil {
		t.Fatal(err)
	}

	got := decodePartsFromMeta(string(metaJSON), "")
	if len(got) != 3 {
		t.Fatalf("parts len = %d, want 3: %+v", len(got), got)
	}
	assertNoStreaming(t, got)

	// Content and structure must survive the scrub.
	if got[0].Text != "interrupted mid-reasoning" {
		t.Fatalf("thinking text lost: %+v", got[0])
	}
	if got[2].Parts[0].Text != "nested still thinking" {
		t.Fatalf("nested thinking text lost: %+v", got[2].Parts[0])
	}
}

func assertNoStreaming(t *testing.T, parts []MessagePart) {
	t.Helper()
	for i, p := range parts {
		if p.Streaming {
			t.Errorf("parts[%d] (%s) still has streaming=true after history decode", i, p.Kind)
		}
		if len(p.Parts) > 0 {
			assertNoStreaming(t, p.Parts)
		}
	}
}
