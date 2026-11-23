package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// StyleMemory writes a fact onto a speaking style.
type StyleMemory interface {
	AppendMemory(id, line string) error
	CreateFromMemory(label, line string) (string, error)
}

var (
	styleMemoryMu sync.RWMutex
	styleMemory   StyleMemory
)

// SetStyleMemory installs the process-wide style memory writer.
func SetStyleMemory(store StyleMemory) {
	styleMemoryMu.Lock()
	styleMemory = store
	styleMemoryMu.Unlock()
}

func currentStyleMemory() StyleMemory {
	styleMemoryMu.RLock()
	defer styleMemoryMu.RUnlock()
	return styleMemory
}

// RegisterRemember adds the model-visible remember tool.
func RegisterRemember(r *Registry) {
	if r == nil {
		return
	}
	r.Register(Tool{
		Name: "remember",
		Description: "Ask to keep one short fact as a speaking style's memory. " +
			"The user chooses an existing style or names a new style before anything is saved. " +
			"Use this when the user wants a preference or durable fact remembered. " +
			"Do not use it for the current task, secrets, or one-off instructions. " +
			"One fact per call, at most 200 characters.",
		Parameters: ObjectSchema(map[string]any{
			"text": StringProp("The single fact to remember, in the user's language"),
		}, []string{"text"}),
		Policy: &ToolPolicy{
			Category:    ToolCategoryInteractive,
			SideEffect:  ToolSideEffectState,
			Risk:        ToolRiskConfirm,
			Parallelism: ToolParallelExclusive,
			TimeoutMS:   -1,
		},
	}, handleRemember)
}

func handleRemember(ctx context.Context, args json.RawMessage) (*CallResult, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	text := strings.Join(strings.Fields(in.Text), " ")
	if text == "" {
		return &CallResult{Content: "记忆内容不能为空。", IsError: true}, nil
	}
	if utf8.RuneCountInString(text) > 200 {
		return &CallResult{Content: "单条记忆不能超过 200 字。", IsError: true}, nil
	}
	writer := currentStyleMemory()
	if writer == nil {
		return &CallResult{Content: "风格记忆不可用，未写入。", IsError: true}, nil
	}
	resp, err := WaitExplicitConfirm(ctx, ConfirmRequest{
		ToolName:  "remember",
		Args:      string(args),
		Reason:    "把这条事实记入某个风格的记忆，或用来新建风格：" + text,
		RiskLevel: "low",
	})
	if err != nil {
		return &CallResult{Content: "确认通道不可用，未写入。", IsError: true}, nil
	}
	if !resp.Approved {
		return &CallResult{Content: "用户没有同意记住这条内容，未写入。"}, nil
	}
	if label := strings.TrimSpace(resp.NewStyleLabel); label != "" {
		id, err := writer.CreateFromMemory(label, text)
		if err != nil {
			return &CallResult{Content: "没有建成新风格：" + err.Error(), IsError: true}, nil
		}
		return &CallResult{Content: fmt.Sprintf("已新建风格 %s，并写入记忆：%s", id, text)}, nil
	}
	if id := strings.TrimSpace(resp.StyleID); id != "" {
		if err := writer.AppendMemory(id, text); err != nil {
			return &CallResult{Content: "没有写入风格记忆：" + err.Error(), IsError: true}, nil
		}
		return &CallResult{Content: fmt.Sprintf("已记入风格 %s：%s", id, text)}, nil
	}
	return &CallResult{Content: "没有选择风格，未写入。"}, nil
}
