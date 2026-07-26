package outbound

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRenderer struct {
	sendChunks []Chunk
	editRefs   []ChatRef
	editIDs    []string
	editChunks []Chunk
	typingRefs []ChatRef
	err        error
	maxLen     int
	dialect    MarkdownDialect
}

func (f *fakeRenderer) Send(ctx context.Context, chunk Chunk) error {
	f.sendChunks = append(f.sendChunks, chunk)
	return f.err
}

func (f *fakeRenderer) Edit(ctx context.Context, ref ChatRef, msgID string, chunk Chunk) error {
	f.editRefs = append(f.editRefs, ref)
	f.editIDs = append(f.editIDs, msgID)
	f.editChunks = append(f.editChunks, chunk)
	return f.err
}

func (f *fakeRenderer) Typing(ctx context.Context, ref ChatRef) error {
	f.typingRefs = append(f.typingRefs, ref)
	return f.err
}

func (f *fakeRenderer) MaxTextLen() int {
	return f.maxLen
}

func (f *fakeRenderer) MarkdownDialect() MarkdownDialect {
	return f.dialect
}

func TestDispatcherRoutesTextEditAndTyping(t *testing.T) {
	renderer := &fakeRenderer{maxLen: 4096}
	dispatcher := NewDispatcher(renderer)
	ref := ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"}

	if err := dispatcher.Dispatch(context.Background(), Chunk{Platform: "feishu", Chat: ref, Kind: "text", Text: "hello"}); err != nil {
		t.Fatalf("dispatch text: %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), Chunk{Platform: "feishu", Chat: ref, Kind: "edit", MsgID: "om_msg", Text: "updated"}); err != nil {
		t.Fatalf("dispatch edit: %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), Chunk{Platform: "feishu", Chat: ref, Kind: "typing"}); err != nil {
		t.Fatalf("dispatch typing: %v", err)
	}

	if len(renderer.sendChunks) != 1 || renderer.sendChunks[0].Text != "hello" {
		t.Fatalf("send chunks = %+v, want hello", renderer.sendChunks)
	}
	if len(renderer.editIDs) != 1 || renderer.editIDs[0] != "om_msg" {
		t.Fatalf("edit ids = %+v, want om_msg", renderer.editIDs)
	}
	if len(renderer.typingRefs) != 1 || renderer.typingRefs[0].ChatID != "oc_group" {
		t.Fatalf("typing refs = %+v, want oc_group", renderer.typingRefs)
	}
}

func TestDispatcherThrottlesRepeatedEdits(t *testing.T) {
	renderer := &fakeRenderer{}
	dispatcher := NewDispatcher(renderer)
	dispatcher.SetEditMinInterval(time.Hour)
	chunk := Chunk{Kind: "edit", MsgID: "om_msg", Chat: ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"}, Text: "updated"}

	if err := dispatcher.Dispatch(context.Background(), chunk); err != nil {
		t.Fatalf("first edit: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := dispatcher.Dispatch(ctx, chunk)
	if err == nil || !strings.Contains(err.Error(), "wait im edit throttle") {
		t.Fatalf("second edit error = %v, want throttle wait error", err)
	}
	if len(renderer.editIDs) != 1 {
		t.Fatalf("edit calls = %d, want only first edit before throttle cancel", len(renderer.editIDs))
	}
}

func TestDispatcherWrapsRendererErrors(t *testing.T) {
	boom := errors.New("boom")
	dispatcher := NewDispatcher(&fakeRenderer{err: boom})

	err := dispatcher.Dispatch(context.Background(), Chunk{Kind: "text", Text: "hello"})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "send im outbound") {
		t.Fatalf("error = %v, want send context", err)
	}
}

func TestDispatcherRejectsTooLongText(t *testing.T) {
	dispatcher := NewDispatcher(&fakeRenderer{maxLen: 3})

	err := dispatcher.Dispatch(context.Background(), Chunk{Kind: "text", Text: "xxxx"})
	if err == nil || !strings.Contains(err.Error(), "exceeds max length 3") {
		t.Fatalf("error = %v, want max length error", err)
	}
}

func TestDispatcherRejectsUnsupportedKind(t *testing.T) {
	dispatcher := NewDispatcher(&fakeRenderer{})

	err := dispatcher.Dispatch(context.Background(), Chunk{Kind: "card"})
	if err == nil || !strings.Contains(err.Error(), "unsupported im outbound kind: card") {
		t.Fatalf("error = %v, want unsupported kind", err)
	}
}
