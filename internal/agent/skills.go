package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/tool"
)

// loadActiveSkills 解析显式 Skill 名称并发送生命周期事件。
// loadActiveSkills resolves explicit Skill names and emits their lifecycle.
// start 回调始终先于 Manager.Load 返回指令，保证用户先看到调用提示。
// The start callback always runs before Manager.Load returns instructions.
func (a *Agent) loadActiveSkills(ctx context.Context, request ChatRequest, emit func(ChatStreamChunk)) (string, error) {
	if len(request.ActiveSkills) == 0 {
		return "", nil
	}
	manager := a.skillManager
	if manager == nil {
		manager = skill.NewManager()
	}
	seen := make(map[string]struct{}, len(request.ActiveSkills))
	var contexts []string
	for _, rawName := range request.ActiveSkills {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		emit(ChatStreamChunk{
			Phase: "skill", SkillName: name, SkillStatus: "start",
			Message: "当前调用 Skill：" + name,
		})
		loaded, err := manager.Load(ctx, skill.LoadRequest{ProjectRoot: request.ProjectRoot, Name: name})
		if err != nil {
			emit(ChatStreamChunk{
				Phase: "skill", SkillName: name, SkillStatus: "error", SkillError: err.Error(),
				Message: fmt.Sprintf("Skill %s 加载失败", name),
			})
			return "", fmt.Errorf("load Skill %q: %w", name, err)
		}
		if len(loaded.Skills) == 0 {
			err := fmt.Errorf("Skill %q returned no instructions", name)
			emit(ChatStreamChunk{Phase: "skill", SkillName: name, SkillStatus: "error", SkillError: err.Error(), Message: err.Error()})
			return "", err
		}
		primary := loaded.Skills[len(loaded.Skills)-1]
		dependencies := make([]string, 0, len(loaded.Skills)-1)
		for _, dependency := range loaded.Skills[:len(loaded.Skills)-1] {
			dependencies = append(dependencies, dependency.Name)
		}
		emit(ChatStreamChunk{
			Phase: "skill", SkillName: name, SkillStatus: "ready", SkillScope: string(primary.Scope),
			SkillSource: primary.Path, SkillDependencies: dependencies,
			Message: fmt.Sprintf("Skill %s 已就绪", name),
		})
		contexts = append(contexts, loaded.Context)
	}
	return strings.Join(contexts, "\n\n---\n\n"), nil
}

func skillChunkFromInvocation(invocation *tool.SkillInvocation, status string) ChatStreamChunk {
	if invocation == nil {
		return ChatStreamChunk{}
	}
	message := "当前调用 Skill：" + invocation.Name
	if status == "ready" {
		message = fmt.Sprintf("Skill %s 已就绪", invocation.Name)
	} else if status == "error" {
		message = fmt.Sprintf("Skill %s 加载失败", invocation.Name)
	}
	return ChatStreamChunk{
		Phase: "skill", Message: message, SkillName: invocation.Name, SkillStatus: status,
		SkillScope: invocation.Scope, SkillSource: invocation.Source,
		SkillDependencies: append([]string(nil), invocation.Dependencies...), SkillError: invocation.Error,
	}
}

// parseSkillLoadCall 识别只读 skill(load) 调用，供 Agent 在执行前发送 start 事件。
// parseSkillLoadCall identifies skill(load) so the Agent can emit start before execution.
func parseSkillLoadCall(toolName, argsJSON string) (string, bool) {
	if toolName != "skill" {
		return "", false
	}
	var args struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", false
	}
	name := strings.TrimSpace(args.Name)
	return name, strings.EqualFold(strings.TrimSpace(args.Action), "load") && name != ""
}

type skillLoadCacheEntry struct {
	done   chan struct{}
	result *tool.CallResult
	err    error
}

// skillLoadCache 对同一回合的同名 Skill 加载做 single-flight 去重。
// skillLoadCache de-duplicates same-name Skill loads within one turn.
type skillLoadCache struct {
	mu      sync.Mutex
	entries map[string]*skillLoadCacheEntry
}

func newSkillLoadCache() *skillLoadCache {
	return &skillLoadCache{entries: make(map[string]*skillLoadCacheEntry)}
}

// markLoaded 将显式激活的 Skill 预填为已加载，使后续工具调用只返回复用提示。
// markLoaded seeds an explicitly activated Skill so later tool calls only return a reuse notice.
func (c *skillLoadCache) markLoaded(name string) {
	done := make(chan struct{})
	close(done)
	c.entries[name] = &skillLoadCacheEntry{
		done: done,
		result: &tool.CallResult{
			SkillInvocation: &tool.SkillInvocation{Name: name, Status: "ready"},
		},
	}
}

// do 只执行首个加载；重复调用等待首个结果并返回轻量提示，避免重复注入正文。
// do runs the first load only; duplicates wait and receive a compact reuse result.
func (c *skillLoadCache) do(name string, load func() (*tool.CallResult, error)) (*tool.CallResult, error) {
	c.mu.Lock()
	if existing, ok := c.entries[name]; ok {
		c.mu.Unlock()
		<-existing.done
		if existing.result == nil {
			return nil, existing.err
		}
		result := *existing.result
		result.Content = fmt.Sprintf("Skill %s was already loaded earlier in this turn; reuse its instructions.", name)
		result.Summary = "Reused Skill " + name
		return &result, existing.err
	}
	entry := &skillLoadCacheEntry{done: make(chan struct{})}
	c.entries[name] = entry
	c.mu.Unlock()

	entry.result, entry.err = load()
	close(entry.done)
	return entry.result, entry.err
}
