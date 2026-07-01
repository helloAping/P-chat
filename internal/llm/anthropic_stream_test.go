package llm

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestAnthropicParseStream_LongDataLine covers the regression
// that motivated the bufio.Scanner → bufio.Reader switch: an
// SSE `data:` line longer than 64 KiB used to silently truncate
// the stream (bufio.MaxScanTokenSize = 64 KiB), causing the
// LLM response to be cut off mid-flight. Anthropic reasoning
// and large image-content blocks both produce lines of this
// size on a regular basis.
//
// The test streams a single 100 KiB data line through
// ParseStream and asserts the full text is recovered.
func TestAnthropicParseStream_LongDataLine(t *testing.T) {
	// Build a 100 KiB payload of valid text content. The
	// content is split across many text_delta events (one
	// per simulated chunk) to mimic Anthropic's real
	// behaviour. The concatenation must arrive intact.
	var buf bytes.Buffer
	want := strings.Repeat("a", 100*1024) // 100 KiB
	ev := fmt.Sprintf(`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}

event: message_stop
data: {"type":"message_stop"}

`, want)
	buf.WriteString(ev)

	a := &AnthropicAdapter{}
	ch := a.ParseStream(&buf)
	var got strings.Builder
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.Done {
			break
		}
		got.WriteString(c.Content)
	}
	if got.Len() != len(want) {
		t.Fatalf("content length = %d, want %d (stream likely truncated by 64KB scanner limit)", got.Len(), len(want))
	}
	if got.String() != want {
		t.Errorf("content mismatch (truncated or altered)")
	}
}

// TestAnthropicParseStream_MultiEvent walks a typical SSE
// stream through the parser and asserts the content +
// thinking deltas are emitted in order. Same as the long-
// line test but exercises the multi-event path.
func TestAnthropicParseStream_MultiEvent(t *testing.T) {
	var buf bytes.Buffer
	// 3 text deltas, 1 thinking delta, message_stop.
	for _, chunk := range []string{"Hello, ", "world", "!"} {
		buf.WriteString(fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", chunk))
	}
	buf.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"deep thought\"}}\n\n")
	buf.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	a := &AnthropicAdapter{}
	ch := a.ParseStream(&buf)
	var content, thinking strings.Builder
	done := false
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.Done {
			done = true
			break
		}
		content.WriteString(c.Content)
		thinking.WriteString(c.Thinking)
	}
	if !done {
		t.Fatal("never saw Done=true")
	}
	if content.String() != "Hello, world!" {
		t.Errorf("content = %q, want %q", content.String(), "Hello, world!")
	}
	if thinking.String() != "deep thought" {
		t.Errorf("thinking = %q, want %q", thinking.String(), "deep thought")
	}
}

// TestAnthropicParseStream_ErrorEvent ensures the "error" SSE
// event is delivered as a StreamChunk.Err (not a content
// delta) so the agent loop can classify it.
func TestAnthropicParseStream_ErrorEvent(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("event: error\ndata: {\"type\":\"error\",\"message\":\"upstream says no\"}\n\n")

	a := &AnthropicAdapter{}
	ch := a.ParseStream(&buf)
	var seenErr error
	for c := range ch {
		if c.Err != nil {
			seenErr = c.Err
		}
	}
	if seenErr == nil {
		t.Fatal("expected an error chunk, got none")
	}
	if !strings.Contains(seenErr.Error(), "upstream says no") {
		t.Errorf("error = %q, want it to contain the upstream message", seenErr.Error())
	}
}

// TestAnthropicParseStream_CRLFTransport verifies the parser
// handles CRLF line endings (some proxies send \r\n instead
// of \n). Anthropic spec says \n, but in practice we get both.
func TestAnthropicParseStream_CRLFTransport(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\r\n\r\n")
	buf.WriteString("event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n")

	a := &AnthropicAdapter{}
	ch := a.ParseStream(&buf)
	var content strings.Builder
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.Done {
			break
		}
		content.WriteString(c.Content)
	}
	if content.String() != "ok" {
		t.Errorf("content = %q, want ok (CRLF transport not handled)", content.String())
	}
}

// TestReadSSELine checks the helper's edge cases: empty
// input, single line, CRLF stripping, partial final line at
// EOF.
func TestReadSSELine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single line", "hello\n", "hello"},
		{"crlf", "hello\r\n", "hello"},
		{"two lines then read one", "first\nsecond\n", "first"},
		{"no trailing newline", "incomplete", "incomplete"},
		{"long line", strings.Repeat("x", 200*1024) + "\n", strings.Repeat("x", 200*1024)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bufio.NewReaderSize(strings.NewReader(c.in), 1<<20)
			got, err := readSSELine(r)
			if err != nil && err != io.EOF {
				t.Fatalf("readSSELine error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
