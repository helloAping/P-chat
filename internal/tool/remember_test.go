package tool

import (
	"context"
	"testing"
)

type fakeStyles struct {
	appended []string
	created  []string
}

func (f *fakeStyles) AppendMemory(id, line string) error {
	f.appended = append(f.appended, id+":"+line)
	return nil
}

func (f *fakeStyles) CreateFromMemory(label, line string) (string, error) {
	f.created = append(f.created, label+":"+line)
	return "custom", nil
}

func rememberCtx(t *testing.T, sessionID string, respond func(ConfirmRequest)) context.Context {
	t.Helper()
	ctx := WithSessionID(context.Background(), sessionID)
	return WithConfirmEmitter(ctx, func(req ConfirmRequest) {
		go respond(req)
	})
}

func TestRememberAppendsChosenStyle(t *testing.T) {
	styles := &fakeStyles{}
	SetStyleMemory(styles)
	t.Cleanup(func() { SetStyleMemory(nil) })
	ctx := rememberCtx(t, "remember-style", func(req ConfirmRequest) {
		SubmitConfirmResponse("remember-style", ConfirmResponse{
			ConfirmID: req.ConfirmID,
			Approved:  true,
			Action:    ConfirmActionOnce,
			StyleID:   "cute",
		})
	})
	result, err := handleRemember(ctx, []byte(`{"text":"提交说明用中文"}`))
	if err != nil || result.IsError || len(styles.appended) != 1 || styles.appended[0] != "cute:提交说明用中文" {
		t.Fatalf("result=%#v appended=%v err=%v", result, styles.appended, err)
	}
}

func TestRememberCreatesStyle(t *testing.T) {
	styles := &fakeStyles{}
	SetStyleMemory(styles)
	t.Cleanup(func() { SetStyleMemory(nil) })
	ctx := rememberCtx(t, "remember-new", func(req ConfirmRequest) {
		SubmitConfirmResponse("remember-new", ConfirmResponse{
			ConfirmID:     req.ConfirmID,
			Approved:      true,
			Action:        ConfirmActionOnce,
			NewStyleLabel: "夜航",
		})
	})
	result, err := handleRemember(ctx, []byte(`{"text":"少用感叹号"}`))
	if err != nil || result.IsError || len(styles.created) != 1 {
		t.Fatalf("result=%#v created=%v err=%v", result, styles.created, err)
	}
}

func TestRememberRejectWritesNothing(t *testing.T) {
	styles := &fakeStyles{}
	SetStyleMemory(styles)
	t.Cleanup(func() { SetStyleMemory(nil) })
	ctx := rememberCtx(t, "remember-no", func(ConfirmRequest) {
		SubmitConfirm("remember-no", false)
	})
	result, err := handleRemember(ctx, []byte(`{"text":"不要"}`))
	if err != nil || result.IsError || len(styles.appended)+len(styles.created) != 0 {
		t.Fatalf("result=%#v styles=%#v err=%v", result, styles, err)
	}
}

func TestRememberWithoutChoiceWritesNothing(t *testing.T) {
	styles := &fakeStyles{}
	SetStyleMemory(styles)
	t.Cleanup(func() { SetStyleMemory(nil) })
	ctx := rememberCtx(t, "remember-empty", func(req ConfirmRequest) {
		SubmitConfirmResponse("remember-empty", ConfirmResponse{
			ConfirmID: req.ConfirmID,
			Approved:  true,
			Action:    ConfirmActionOnce,
		})
	})
	result, err := handleRemember(ctx, []byte(`{"text":"悬空"}`))
	if err != nil || result.IsError || len(styles.appended)+len(styles.created) != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
