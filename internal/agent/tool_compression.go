package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/p-chat/pchat/internal/tool"
)

type compressedToolCall struct {
	coveredBy   int
	fingerprint string
}

func buildSubagentToolCompressionPlan(req ChatRequest, calls []nativeToolCall) map[int]compressedToolCall {
	if req.SubagentType == "" || len(calls) < 2 {
		return nil
	}
	type candidate struct {
		idx  int
		rank int
	}
	groups := make(map[string][]candidate)
	best := make(map[string]candidate)
	for i, call := range calls {
		fp, rank, ok := execOperationFingerprintInfo(call)
		if !ok {
			continue
		}
		c := candidate{idx: i, rank: rank}
		groups[fp] = append(groups[fp], c)
		if b, exists := best[fp]; !exists || c.rank < b.rank {
			best[fp] = c
		}
	}
	plan := make(map[int]compressedToolCall)
	for fp, candidates := range groups {
		if len(candidates) < 2 {
			continue
		}
		representative := best[fp].idx
		for _, c := range candidates {
			if c.idx == representative {
				continue
			}
			plan[c.idx] = compressedToolCall{coveredBy: representative, fingerprint: fp}
		}
	}
	if len(plan) == 0 {
		return nil
	}
	return plan
}

func compressedToolResult(c compressedToolCall) *tool.CallResult {
	return &tool.CallResult{
		Content: fmt.Sprintf("[compressed] This read-only sub-agent command was not re-run. It is covered by tool call %d with the same operation fingerprint (%s). Use that prior result; only call another tool if it targets different evidence.", c.coveredBy+1, c.fingerprint),
		Summary: "Compressed duplicate read-only sub-agent command",
	}
}

type execFingerprintArgs struct {
	Command    string `json:"command"`
	WorkDir    string `json:"work_dir,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
	Background bool   `json:"background,omitempty"`
}

var (
	psCommandRe        = regexp.MustCompile(`(?i)\b(?:powershell|pwsh)(?:\.exe)?\b.*?\s-(?:command|c)\s+(.+)$`)
	goTestRe           = regexp.MustCompile(`(?i)\bgo\s+test\b(.+)$`)
	getContentRe       = regexp.MustCompile(`(?i)(?:\$\w+\s*=\s*)?get-content\b([^;\r\n|]*)`)
	selectStringRe     = regexp.MustCompile(`(?i)\bselect-string\b([^;\r\n|]*)`)
	psDisplayTailRe    = regexp.MustCompile(`(?i)\|\s*(select-object|select-string|foreach-object|write-host)\b.*$`)
	shellRedirectRe    = regexp.MustCompile(`(?i)\s+\d?>&\d+|\s+\d?>\s*\S+`)
	psArraySliceTailRe = regexp.MustCompile(`\[[\d\s.]+\]$`)
)

func execOperationFingerprint(call nativeToolCall) (string, bool) {
	fp, _, ok := execOperationFingerprintInfo(call)
	return fp, ok
}

func execOperationFingerprintInfo(call nativeToolCall) (string, int, bool) {
	if call.Name != "exec_command" {
		return "", 0, false
	}
	var args execFingerprintArgs
	if json.Unmarshal([]byte(call.ArgsJSON), &args) != nil || strings.TrimSpace(args.Command) == "" {
		return "", 0, false
	}
	if args.DryRun || args.Background {
		return "", 0, false
	}
	cmd := unwrapExecCommand(args.Command)
	if fp, ok := fingerprintGoTest(cmd, args.WorkDir); ok {
		return fp, representativeRank(cmd), true
	}
	if fp, ok := fingerprintGetContent(cmd, args.WorkDir); ok {
		return fp, representativeRank(cmd), true
	}
	if fp, ok := fingerprintSelectString(cmd, args.WorkDir); ok {
		return fp, representativeRank(cmd), true
	}
	return "", 0, false
}

func representativeRank(command string) int {
	lower := strings.ToLower(command)
	rank := 0
	if strings.Contains(lower, "|") {
		rank += 2
	}
	if strings.Contains(lower, "select-string") || strings.Contains(lower, "findstr") {
		rank += 2
	}
	if strings.Contains(lower, "[") && strings.Contains(lower, "]") {
		rank++
	}
	return rank
}

func unwrapExecCommand(command string) string {
	s := strings.TrimSpace(command)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "chcp 65001") {
		if i := strings.Index(s, "&"); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	if m := psCommandRe.FindStringSubmatch(s); len(m) == 2 {
		s = strings.TrimSpace(m[1])
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.TrimSpace(s)
	return s
}

func fingerprintGoTest(command, workDir string) (string, bool) {
	base := psDisplayTailRe.ReplaceAllString(command, "")
	base = shellRedirectRe.ReplaceAllString(base, "")
	m := goTestRe.FindStringSubmatch(base)
	if len(m) != 2 {
		return "", false
	}
	fields := splitCommandFields(m[1])
	var kept []string
	for i := 0; i < len(fields); i++ {
		f := strings.TrimSpace(fields[i])
		if f == "" {
			continue
		}
		lf := strings.ToLower(f)
		switch lf {
		case "-run", "-count", "-timeout", "-coverprofile", "-coverpkg", "-cpu", "-bench", "-benchtime":
			i++
			continue
		case "-v", "-json":
			continue
		case "2>&1", "1>&2":
			continue
		}
		if strings.HasPrefix(lf, "-run=") || strings.HasPrefix(lf, "-count=") || strings.HasPrefix(lf, "-timeout=") ||
			strings.HasPrefix(lf, "-coverprofile=") || strings.HasPrefix(lf, "-coverpkg=") ||
			strings.HasPrefix(lf, "-cpu=") || strings.HasPrefix(lf, "-bench=") || strings.HasPrefix(lf, "-benchtime=") {
			continue
		}
		if strings.HasPrefix(f, "-") {
			kept = append(kept, lf)
			continue
		}
		kept = append(kept, normalizeFingerprintPath(f))
	}
	if len(kept) == 0 {
		kept = []string{"."}
	}
	sort.Strings(kept)
	return "verify:go-test:" + normalizeFingerprintPath(workDir) + ":" + strings.Join(kept, ","), true
}

func fingerprintGetContent(command, workDir string) (string, bool) {
	m := getContentRe.FindStringSubmatch(command)
	if len(m) != 2 {
		return "", false
	}
	path := firstPathFromGetContentSegment(m[1])
	if path == "" {
		return "", false
	}
	path = psArraySliceTailRe.ReplaceAllString(path, "")
	return "read-file:" + normalizeFingerprintPath(workDir) + ":" + normalizeFingerprintPath(path), true
}

func fingerprintSelectString(command, workDir string) (string, bool) {
	m := selectStringRe.FindStringSubmatch(command)
	if len(m) != 2 {
		return "", false
	}
	fields := splitCommandFields(m[1])
	paths := normalizeFingerprintList(paramValue(fields, "-path"))
	patterns := normalizeFingerprintList(paramValue(fields, "-pattern"))
	if len(paths) == 0 || len(patterns) == 0 {
		return "", false
	}
	sort.Strings(paths)
	sort.Strings(patterns)
	return "search-file:" + normalizeFingerprintPath(workDir) + ":" + strings.Join(paths, ",") + ":pattern=" + strings.Join(patterns, ","), true
}

func firstPathFromGetContentSegment(segment string) string {
	fields := splitCommandFields(segment)
	for i := 0; i < len(fields); i++ {
		f := strings.TrimSpace(fields[i])
		if f == "" {
			continue
		}
		lf := strings.ToLower(f)
		if strings.HasPrefix(lf, "-path=") || strings.HasPrefix(lf, "-literalpath=") {
			if v := strings.TrimSpace(f[strings.IndexByte(f, '=')+1:]); v != "" {
				return v
			}
			continue
		}
		if lf == "-path" || lf == "-literalpath" || lf == "-encoding" || lf == "-totalcount" || lf == "-tail" {
			i++
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		return f
	}
	return ""
}

func paramValue(fields []string, name string) string {
	name = strings.ToLower(name)
	for i := 0; i < len(fields); i++ {
		f := strings.TrimSpace(fields[i])
		lf := strings.ToLower(f)
		if lf == name && i+1 < len(fields) {
			return fields[i+1]
		}
		if strings.HasPrefix(lf, name+"=") {
			return f[len(name)+1:]
		}
	}
	return ""
}

func normalizeFingerprintList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		n := normalizeFingerprintPath(part)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func normalizeFingerprintPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimRight(value, ";")
	value = strings.ToLower(value)
	return value
}

func splitCommandFields(s string) []string {
	var fields []string
	var b strings.Builder
	var quote rune
	flush := func() {
		if b.Len() == 0 {
			return
		}
		fields = append(fields, b.String())
		b.Reset()
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\r' || r == '\n':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return fields
}
