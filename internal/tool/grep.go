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
	Pattern        string `json:"pattern"`
	Path           string `json:"path,omitempty"`
	CaseSensitive  bool   `json:"case_sensitive,omitempty"`
	TopK           int    `json:"top_k,omitempty"`
	Base           string `json:"base,omitempty"`
}

// RegisterGrep registers the grep tool for keyword-based file search
// within the session's working directory (project root). When the
// project root is absent, it falls back to searching the configured
// knowledge bases, preserving the pre-2026-08 behaviour for users who
// relied on grep as a KB search tool.
func RegisterGrep(r *Registry, cfg *config.Config) {
	r.Register(Tool{
		Name: "grep",
		Description: "在本地文件中精确搜索关键词/字符串。默认在工作目录（项目根目录）下递归搜索源代码和文档；" +
			"若当前会话未设置项目根目录，则退化为在知识库中搜索。path 参数可指定子目录或文件。",
		Parameters: ObjectSchema(map[string]any{
			"pattern":        StringProp("要搜索的关键词或字符串"),
			"path":           StringProp("搜索范围：工作目录下的相对路径（子目录或单个文件）。留空表示整个工作目录"),
			"case_sensitive": BoolProp("是否区分大小写。默认不区分（false）"),
			"top_k": map[string]any{
				"type":        "integer",
				"description": "最大返回结果数，默认 10",
				"minimum":     1,
				"maximum":     20,
			},
			"base": StringProp("知识库名称（可选，仅当未设置项目根目录、退化到知识库搜索时生效）"),
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

		root := projectRootFromCtx(ctx)
		if root == "" {
			// No project root: fall back to knowledge-base search
			// (legacy behaviour).
			if cfg == nil || !cfg.Knowledge.Enabled {
				return &CallResult{Content: "未设置项目根目录，且知识库未启用，无法搜索", IsError: true}, nil
			}
			return grepKnowledgeBasesResult(ctx, cfg, a.Base, a.Pattern, a.TopK), nil
		}

		// Search the working directory (project root).
		searchDir := root
		if a.Path != "" {
			searchDir = filepath.Join(root, a.Path)
		}
		results := grepWorkingDir(ctx, searchDir, root, a.Pattern, a.CaseSensitive, a.TopK)
		return formatGrepResults(a.Pattern, results), nil
	}
}

// grepWorkingDir walks searchDir (which must be under root) and returns
// up to maxResults lines matching pattern. Binary files, hidden dirs,
// and vendor/node_modules/.git are skipped. When caseSensitive is
// false, matching is case-insensitive.
func grepWorkingDir(ctx context.Context, searchDir, root, pattern string, caseSensitive bool, maxResults int) []string {
	results := make([]string, 0, maxResults)
	pat := pattern
	if !caseSensitive {
		pat = strings.ToLower(pattern)
	}

	_ = filepath.Walk(searchDir, func(path string, info os.FileInfo, walkErr error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if walkErr != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
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
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			if lineNo%64 == 0 && ctx.Err() != nil {
				f.Close()
				return filepath.SkipAll
			}
			lineNo++
			line := scanner.Text()
			matched := strings.Contains(line, pattern)
			if !caseSensitive {
				matched = strings.Contains(strings.ToLower(line), pat)
			}
			if matched {
				// Report the path relative to the project root for brevity.
				rel := path
				if relRoot, err := filepath.Rel(root, path); err == nil {
					rel = relRoot
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", rel, lineNo, strings.TrimSpace(line)))
				if len(results) >= maxResults {
					f.Close()
					return filepath.SkipAll
				}
			}
		}
		f.Close()
		return nil
	})
	return results
}

func formatGrepResults(pattern string, results []string) *CallResult {
	if len(results) == 0 {
		return &CallResult{Content: fmt.Sprintf("(未找到包含 \"%s\" 的内容)", pattern)}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## grep: \"%s\" (%d 条结果)\n\n", pattern, len(results))
	for _, r := range results {
		b.WriteString(r)
		b.WriteString("\n\n")
	}
	return &CallResult{Content: b.String()}
}

// grepKnowledgeBasesResult wraps grepKnowledgeBases into a CallResult.
func grepKnowledgeBasesResult(ctx context.Context, cfg *config.Config, baseName, pattern string, maxResults int) *CallResult {
	results := grepKnowledgeBases(ctx, cfg, baseName, pattern, maxResults)
	if len(results) == 0 {
		return &CallResult{Content: fmt.Sprintf("(在知识库中未找到包含 \"%s\" 的文件)", pattern)}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## grep: \"%s\" (%d 条结果)\n\n", pattern, len(results))
	for _, r := range results {
		content := strings.TrimSpace(r.Content)
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		fmt.Fprintf(&b, "%s\n    %s\n\n", r.Source, content)
	}
	return &CallResult{Content: b.String()}
}

// grepKnowledgeBases searches knowledge-base files for lines matching
// pattern (case-insensitive). Returns file:line results.
// If baseName is non-empty and not "__all__", searches only that base.
// Respects each base's ExcludePatterns to skip matching paths.
func grepKnowledgeBases(ctx context.Context, cfg *config.Config, baseName, pattern string, maxResults int) []knowledge.SearchResult {
	if cfg == nil {
		return nil
	}
	kc := cfg.Knowledge
	if pattern == "" || maxResults <= 0 || !kc.Enabled {
		return nil
	}
	bases := resolveBases(kc, baseName)
	if len(bases) == 0 {
		return nil
	}

	patternLower := strings.ToLower(pattern)
	var out []knowledge.SearchResult

	for _, base := range bases {
		// Allow cancellation between bases.
		if err := ctx.Err(); err != nil {
			break
		}
		absPath, err := filepath.Abs(base.Path)
		if err != nil {
			log.Printf("[grep] abs path %s: %v", base.Path, err)
			continue
		}
		_ = filepath.Walk(absPath, func(path string, info os.FileInfo, walkErr error) error {
			// Allow cancellation between files.
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if walkErr != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				name := info.Name()
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
			// Check base-level exclude patterns.
			if len(base.ExcludePatterns) > 0 {
				rel, err := filepath.Rel(absPath, path)
				if err == nil {
					for _, pat := range base.ExcludePatterns {
						matched, _ := filepath.Match(pat, rel)
						if matched {
							return nil
						}
					}
				}
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}

			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			lineNo := 0
			for scanner.Scan() {
				// Allow cancellation every 64 lines (amortised).
				if lineNo%64 == 0 && ctx.Err() != nil {
					f.Close()
					return filepath.SkipAll
				}
				lineNo++
				if strings.Contains(strings.ToLower(scanner.Text()), patternLower) {
					out = append(out, knowledge.SearchResult{
						Source:     fmt.Sprintf("%s:%d", path, lineNo),
						Content:    strings.TrimSpace(scanner.Text()),
						Similarity: 1.0,
						Rank:       len(out) + 1,
					})
					if len(out) >= maxResults {
						f.Close()
						return filepath.SkipAll
					}
				}
			}
			f.Close()
			return nil
		})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}
