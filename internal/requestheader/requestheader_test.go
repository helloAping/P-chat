package requestheader

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/p-chat/pchat/internal/trace"
)

func TestApplyExpandsDynamicValues(t *testing.T) {
	ctx := WithContext(context.Background(), "conversation-42", 73)
	ctx = trace.WithID(ctx, "trace-99")
	headers := http.Header{}
	templates := map[string]string{
		"X-Conversation": "{{conversation_id}}",
		"X-Session":      "{{session_id}}",
		"X-Message":      "msg-{{message_id}}",
		"X-Trace":        "{{trace_id}}",
		"X-UUID":         "{{uuid}}",
		"X-UUID-Copy":    "prefix-{{uuid}}",
		"X-Snowflake":    "{{snowflake_id}}",
		"X-Timestamp":    "{{timestamp}}",
		"X-Timestamp-Ms": "{{timestamp_ms}}",
	}

	if err := Apply(ctx, headers, templates); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := headers.Get("X-Conversation"); got != "conversation-42" {
		t.Fatalf("X-Conversation = %q", got)
	}
	if got := headers.Get("X-Session"); got != "conversation-42" {
		t.Fatalf("X-Session = %q", got)
	}
	if got := headers.Get("X-Message"); got != "msg-73" {
		t.Fatalf("X-Message = %q", got)
	}
	if got := headers.Get("X-Trace"); got != "trace-99" {
		t.Fatalf("X-Trace = %q", got)
	}
	requestUUID := headers.Get("X-UUID")
	if _, err := uuid.Parse(requestUUID); err != nil {
		t.Fatalf("X-UUID = %q, parse error = %v", requestUUID, err)
	}
	if got := headers.Get("X-UUID-Copy"); got != "prefix-"+requestUUID {
		t.Fatalf("X-UUID-Copy = %q, want shared request UUID", got)
	}
	if _, err := strconv.ParseUint(headers.Get("X-Snowflake"), 10, 64); err != nil {
		t.Fatalf("X-Snowflake = %q, parse error = %v", headers.Get("X-Snowflake"), err)
	}
	seconds, err := strconv.ParseInt(headers.Get("X-Timestamp"), 10, 64)
	if err != nil || seconds <= 0 {
		t.Fatalf("X-Timestamp = %q", headers.Get("X-Timestamp"))
	}
	milliseconds, err := strconv.ParseInt(headers.Get("X-Timestamp-Ms"), 10, 64)
	if err != nil || milliseconds < seconds*1000 {
		t.Fatalf("X-Timestamp-Ms = %q", headers.Get("X-Timestamp-Ms"))
	}
}

func TestApplyWithoutConversationUsesEmptyRequestValues(t *testing.T) {
	headers := http.Header{}
	if err := Apply(context.Background(), headers, map[string]string{
		"X-Conversation": "{{conversation_id}}",
		"X-Message":      "{{message_id}}",
	}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := headers.Get("X-Conversation"); got != "" {
		t.Fatalf("X-Conversation = %q", got)
	}
	if got := headers.Get("X-Message"); got != "" {
		t.Fatalf("X-Message = %q", got)
	}
}

func TestApplyAcceptsNativeStringMessageIdentifier(t *testing.T) {
	ctx := WithIdentifiers(context.Background(), "im:feishu:chat-1", "om_native_message_1")
	headers := http.Header{}
	if err := Apply(ctx, headers, map[string]string{"X-Message": "{{message_id}}"}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := headers.Get("X-Message"); got != "om_native_message_1" {
		t.Fatalf("X-Message = %q", got)
	}
}

func TestValidateTemplatesRejectsUnsafeOrUnknownValues(t *testing.T) {
	tests := []struct {
		name      string
		templates map[string]string
		want      string
	}{
		{name: "invalid name", templates: map[string]string{"Bad Header": "value"}, want: "invalid header name"},
		{name: "reserved header", templates: map[string]string{"Content-Length": "10"}, want: "reserved header"},
		{name: "newline", templates: map[string]string{"X-Test": "first\r\nInjected: yes"}, want: "line break"},
		{name: "control character", templates: map[string]string{"X-Test": "value\x00"}, want: "control character"},
		{name: "unknown placeholder", templates: map[string]string{"X-Test": "{{random_id}}"}, want: "unknown placeholder"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTemplates(tt.templates)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateTemplates() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateTemplatesAllowsAuthOverride(t *testing.T) {
	if err := ValidateTemplates(map[string]string{
		"Authorization": "Bearer custom",
		"x-api-key":     "custom",
		"Content-Type":  "application/json",
	}); err != nil {
		t.Fatalf("ValidateTemplates() error = %v", err)
	}
}
