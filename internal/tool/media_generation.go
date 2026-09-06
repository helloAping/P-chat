package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/generation"
)

type mediaGenerationArgs struct {
	Operation config.GenerationOperation `json:"operation"`
	Prompt    string                     `json:"prompt"`
	InputRefs []string                   `json:"input_refs,omitempty"`
	Options   map[string]any             `json:"options,omitempty"`
}

func handleGenerateImage(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	return handleMediaGeneration(ctx, "generate_image", config.MediaImage, argsRaw)
}

func handleGenerateVideo(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	return handleMediaGeneration(ctx, "generate_video", config.MediaVideo, argsRaw)
}

func handleGenerateAudio(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	return handleMediaGeneration(ctx, "generate_audio", config.MediaAudio, argsRaw)
}

func handleMediaGeneration(ctx context.Context, toolName string, outputKind config.MediaKind, argsRaw json.RawMessage) (*CallResult, error) {
	var args mediaGenerationArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return mediaGenerationError(CallStatusError, "媒体生成参数无效: "+err.Error()), nil
	}
	if !args.Operation.IsValid() {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("不支持的媒体生成能力 %q", args.Operation)), nil
	}
	if args.Operation.OutputKind() != outputKind {
		return mediaGenerationError(CallStatusBlocked, fmt.Sprintf("能力 %s 不属于 %s，调用已拒绝", args.Operation, toolName)), nil
	}

	// 工具内部是最终授权边界。即使旧消息、文本 tool_call 或直接 registry
	// 调用绕过了模型可见性过滤，这里仍必须在任何执行器调用前拒绝。
	// The handler is the final authority boundary. Visibility filtering is only
	// an optimization and stale/forged calls still stop here before execution.
	access, ok := generation.AccessFrom(ctx)
	if !ok || !access.IsEnabled(args.Operation) {
		return mediaGenerationError(CallStatusBlocked, fmt.Sprintf("媒体生成能力 %s 已在当前会话关闭；请在会话设置中启用后重试", args.Operation)), nil
	}
	target, ok := access.Target(args.Operation)
	if !ok {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("媒体生成能力 %s 已启用，但没有可用模型；请在应用设置中配置该能力的默认模型", args.Operation)), nil
	}
	dispatch, ok := access.Dispatch(args.Operation)
	if !ok {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("媒体生成能力 %s 的供应商路由不可用；请重新保存模型配置", args.Operation)), nil
	}
	if access.Executor == nil {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("媒体生成能力 %s 的执行器不可用；请检查模型适配器配置", args.Operation)), nil
	}

	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		return mediaGenerationError(CallStatusError, "prompt 不能为空"), nil
	}
	inputRefs := generation.NormalizeInputRefs(args.InputRefs)
	requiredInput := args.Operation.RequiredInputKind()
	if requiredInput.IsValid() && len(inputRefs) == 0 {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("能力 %s 需要至少一个 input_refs 附件或媒体资产 ID", args.Operation)), nil
	}
	if !requiredInput.IsValid() && len(inputRefs) > 0 {
		return mediaGenerationError(CallStatusBlocked, fmt.Sprintf("能力 %s 不接受媒体 input_refs；请改用与附件类型匹配的生成能力", args.Operation)), nil
	}
	if maximum := generation.MaxInputRefs(args.Operation); len(inputRefs) > maximum {
		return mediaGenerationError(CallStatusBlocked, fmt.Sprintf("能力 %s 最多接受 %d 个 input_refs，本次提供了 %d 个", args.Operation, maximum, len(inputRefs))), nil
	}
	for _, ref := range inputRefs {
		if !validGenerationReference(ref) {
			return mediaGenerationError(CallStatusBlocked, fmt.Sprintf("input_refs 只能传附件或媒体资产 ID，不能传路径、URL 或 base64: %q", ref)), nil
		}
	}
	normalizedOptions, err := generation.NormalizeOptions(args.Operation, args.Options)
	if err != nil {
		return mediaGenerationError(CallStatusBlocked, "媒体生成 options 已拒绝: "+err.Error()), nil
	}

	sessionID, _ := ctx.Value(SessionIDKey{}).(string)
	result, err := access.Executor.Generate(ctx, generation.Request{
		SessionID: sessionID,
		ToolName:  toolName,
		Operation: args.Operation,
		Target:    target,
		Prompt:    prompt,
		InputRefs: inputRefs,
		Options:   normalizedOptions,
		Dispatch:  dispatch,
	})
	if err != nil {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("%s 调用失败: %v", toolName, err)), nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return mediaGenerationError(CallStatusError, fmt.Sprintf("%s 返回结果无法编码: %v", toolName, err)), nil
	}
	return &CallResult{
		Content: string(encoded),
		Status:  CallStatusOK,
		Summary: fmt.Sprintf("%s 已提交，状态 %s", args.Operation, result.Status),
	}, nil
}

// FilterMediaGenerationTools removes disabled generation families and narrows
// each remaining operation enum to this request's explicit session switches.
func FilterMediaGenerationTools(tools []Tool, enabled []config.GenerationOperation) []Tool {
	byKind := make(map[config.MediaKind][]config.GenerationOperation, 3)
	seen := make(map[config.GenerationOperation]struct{}, len(enabled))
	for _, operation := range enabled {
		if !operation.IsValid() {
			continue
		}
		if _, ok := seen[operation]; ok {
			continue
		}
		seen[operation] = struct{}{}
		byKind[operation.OutputKind()] = append(byKind[operation.OutputKind()], operation)
	}
	out := make([]Tool, 0, len(tools))
	for _, candidate := range tools {
		kind := config.MediaKind("")
		switch candidate.Name {
		case "generate_image":
			kind = config.MediaImage
		case "generate_video":
			kind = config.MediaVideo
		case "generate_audio":
			kind = config.MediaAudio
		default:
			out = append(out, candidate)
			continue
		}
		operations := byKind[kind]
		if len(operations) == 0 {
			continue
		}
		candidate.Parameters = generationToolSchema(operations...)
		out = append(out, candidate)
	}
	return out
}

func mediaGenerationError(status CallStatus, message string) *CallResult {
	return &CallResult{Content: message, IsError: true, Status: status, Summary: message}
}

func validGenerationReference(ref string) bool {
	if ref == "" || len(ref) > 256 {
		return false
	}
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return false
	}
	return !strings.ContainsAny(ref, `/\\`)
}

func generationToolSchema(operations ...config.GenerationOperation) json.RawMessage {
	values := make([]string, 0, len(operations))
	for _, operation := range operations {
		values = append(values, string(operation))
	}
	return ObjectSchema(map[string]any{
		"operation": StringEnumProp("Canonical generation capability selected for this request", values...),
		"prompt":    StringProp("Final generation instruction prepared from the user's request and relevant conversation context."),
		"input_refs": map[string]any{
			"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 4,
			"description": "Conversation attachment or generated-asset IDs only. Never pass paths, URLs, or base64.",
		},
		"options": map[string]any{
			"type": "object", "description": "Bounded vendor-neutral hints only. Provider-specific fields belong in application settings.",
			"additionalProperties": false,
			"properties": map[string]any{
				"seed":             map[string]any{"type": "integer", "minimum": 0, "maximum": 2147483647},
				"count":            map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
				"negative_prompt":  map[string]any{"type": "string", "maxLength": 4000},
				"aspect_ratio":     map[string]any{"type": "string", "maxLength": 64},
				"width":            map[string]any{"type": "integer", "minimum": 64, "maximum": 8192},
				"height":           map[string]any{"type": "integer", "minimum": 64, "maximum": 8192},
				"size":             map[string]any{"type": "string", "maxLength": 64},
				"resolution":       map[string]any{"type": "string", "maxLength": 64},
				"duration":         map[string]any{"type": "integer", "minimum": 1, "maximum": 600},
				"duration_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600},
				"quality":          map[string]any{"type": "string", "maxLength": 64},
				"style":            map[string]any{"type": "string", "maxLength": 64},
				"voice":            map[string]any{"type": "string", "maxLength": 128},
				"voice_id":         map[string]any{"type": "string", "maxLength": 128},
				"speed":            map[string]any{"type": "number", "minimum": 0.25, "maximum": 4},
				"volume":           map[string]any{"type": "number", "minimum": 0, "maximum": 10},
				"pitch":            map[string]any{"type": "number", "minimum": -12, "maximum": 12},
				"sample_rate":      map[string]any{"type": "integer", "minimum": 8000, "maximum": 192000},
				"format":           StringEnumProp("Generated audio format", "mp3", "wav", "pcm", "flac", "aac", "ogg"),
			},
		},
	}, []string{"operation", "prompt"})
}
