package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
)

type grepArgs struct {
	Pattern string `json:"pattern"`
	TopK    int    `json:"top_k,omitempty"`
	Base    string `json:"base,omitempty"`
}

// RegisterGrep registers the grep tool for keyword-based file search
// within knowledge-base directories.
func RegisterGrep(r *Registry, cfg *config.Config) {
	r.Register(Tool{
		Name: "grep",
		Description: "在本地知识库文件中精确搜索关键词/字符串。base 参数指定知识库名称（可选）。当你需要找特定的函数名、变量名、类名、或任何精确文本时使用。",
		Parameters: ObjectSchema(map[string]any{
			"pattern": StringProp("要搜索的关键词或字符串"),
			"top_k": map[string]any{
				"type":        "integer",
				"description": "最大返回结果数，默认 10",
				"minimum":     1,
				"maximum":     20,
			},
			"base": StringProp("知识库名称（可选，留空搜索全部启用的知识库）"),
		}, []string{"pattern"}),
	}, makeGrepHandler(cfg))
}

func makeGrepHandler(cfg *config.Config) ToolHandler {
	return func(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
		var a grepArgs
		if err := json.Unmarshal(argsRaw, &a); err != nil {
			return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
		}
		if a.Pattern == "" {
			return &CallResult{Content: "pattern 不能为空", IsError: true}, nil
		}
		if a.TopK <= 0 {
			a.TopK = 10
		}
		if a.TopK > 20 {
			a.TopK = 20
		}

		kc := cfg.Knowledge
		if !kc.Enabled {
			return &CallResult{Content: "知识库未启用", IsError: true}, nil
		}

		results := grepKnowledgeBases(cfg, a.Base, a.Pattern, a.TopK)
		if len(results) == 0 {
			return &CallResult{Content: fmt.Sprintf("(在知识库中未找到包含 \"%s\" 的文件)", a.Pattern)}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "## grep: \"%s\" (%d 条结果)\n\n", a.Pattern, len(results))
		for _, r := range results {
			content := strings.TrimSpace(r.Content)
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fmt.Fprintf(&b, "%s\n    %s\n\n", r.Source, content)
		}
		return &CallResult{Content: b.String()}, nil
	}
}

// grepKnowledgeBases searches knowledge-base files for lines matching
// pattern (case-insensitive). Returns file:line results.
// If baseName is non-empty and not "__all__", searches only that base.
func grepKnowledgeBases(cfg *config.Config, baseName, pattern string, maxResults int) []knowledge.SearchResult {
	if pattern == "" || maxResults <= 0 {
		return nil
	}
	kc := cfg.Knowledge
	bases := resolveBases(kc, baseName)
	if len(bases) == 0 {
		return nil
	}

	patternLower := strings.ToLower(pattern)
	var out []knowledge.SearchResult

	for _, base := range bases {
		absPath, err := filepath.Abs(base.Path)
		if err != nil {
			log.Printf("[grep] abs path %s: %v", base.Path, err)
			continue
		}
		_ = filepath.Walk(absPath, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.IsDir() {
				name := filepath.Base(path)
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !knowledge.IndexableExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			if info.Size() > 5*1024*1024 {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()

			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			lineNo := 0
			for scanner.Scan() {
				lineNo++
				if strings.Contains(strings.ToLower(scanner.Text()), patternLower) {
					out = append(out, knowledge.SearchResult{
						Source:     fmt.Sprintf("%s:%d", path, lineNo),
						Content:    strings.TrimSpace(scanner.Text()),
						Similarity: 1.0,
						Rank:       len(out) + 1,
					})
					if len(out) >= maxResults {
						return filepath.SkipAll
					}
				}
			}
			return nil
		})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}
