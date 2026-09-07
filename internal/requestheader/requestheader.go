// Package requestheader 校验并展开供应商自定义的 HTTP 请求头。
// Package requestheader validates and expands provider-defined HTTP headers.
package requestheader

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/p-chat/pchat/internal/trace"
)

const (
	maxHeaderCount        = 32
	maxHeaderNameBytes    = 128
	maxHeaderValueBytes   = 4096
	snowflakeSequenceMask = uint16(0x0fff)
	snowflakeNodeMask     = uint64(0x03ff)
)

var (
	placeholderPattern  = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
	allowedPlaceholders = map[string]struct{}{
		"conversation_id": {},
		"session_id":      {},
		"message_id":      {},
		"trace_id":        {},
		"uuid":            {},
		"snowflake_id":    {},
		"timestamp":       {},
		"timestamp_ms":    {},
	}
	reservedHeaders = map[string]struct{}{
		"connection":          {},
		"content-length":      {},
		"host":                {},
		"keep-alive":          {},
		"proxy-authenticate":  {},
		"proxy-authorization": {},
		"proxy-connection":    {},
		"te":                  {},
		"trailer":             {},
		"transfer-encoding":   {},
		"upgrade":             {},
	}
	snowflakeState = struct {
		sync.Mutex
		lastMillis int64
		sequence   uint16
		node       uint64
	}{node: snowflakeNodeID()}
)

type contextKey struct{}

type requestContext struct {
	conversationID string
	messageID      string
}

// WithContext 把当前对话与消息标识加入上游请求上下文。
// WithContext attaches conversation and message identifiers to an upstream request.
func WithContext(ctx context.Context, conversationID string, messageID int64) context.Context {
	messageValue := ""
	if messageID > 0 {
		messageValue = strconv.FormatInt(messageID, 10)
	}
	return WithIdentifiers(ctx, conversationID, messageValue)
}

// WithIdentifiers 把字符串形式的当前对话与消息标识加入上游请求上下文。
// WithIdentifiers attaches string-form conversation and message identifiers to an upstream request.
func WithIdentifiers(ctx context.Context, conversationID, messageID string) context.Context {
	return context.WithValue(ctx, contextKey{}, requestContext{
		conversationID: conversationID,
		messageID:      messageID,
	})
}

// CloneTemplates 返回可安全修改的自定义请求头副本。
// CloneTemplates returns an independently mutable copy of custom header templates.
func CloneTemplates(templates map[string]string) map[string]string {
	if len(templates) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(templates))
	for name, value := range templates {
		cloned[name] = value
	}
	return cloned
}

// ValidateTemplates 校验请求头名称、值、动态变量与传输层保留字段。
// ValidateTemplates validates names, values, placeholders, and transport-reserved fields.
func ValidateTemplates(templates map[string]string) error {
	if len(templates) > maxHeaderCount {
		return fmt.Errorf("custom_headers has %d entries; maximum is %d", len(templates), maxHeaderCount)
	}
	seen := make(map[string]struct{}, len(templates))
	for name, value := range templates {
		if len(name) == 0 || len(name) > maxHeaderNameBytes || !validHeaderName(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		lowerName := strings.ToLower(name)
		if _, reserved := reservedHeaders[lowerName]; reserved {
			return fmt.Errorf("custom header %q is a reserved header managed by the HTTP transport", name)
		}
		canonicalName := http.CanonicalHeaderKey(name)
		if _, duplicate := seen[canonicalName]; duplicate {
			return fmt.Errorf("duplicate custom header %q", canonicalName)
		}
		seen[canonicalName] = struct{}{}
		if len(value) > maxHeaderValueBytes {
			return fmt.Errorf("custom header %q value exceeds %d bytes", name, maxHeaderValueBytes)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("custom header %q value must not contain a line break", name)
		}
		if !validHeaderValue(value) {
			return fmt.Errorf("custom header %q value contains a control character", name)
		}
		matches := placeholderPattern.FindAllStringSubmatch(value, -1)
		for _, match := range matches {
			placeholder := strings.TrimSpace(match[1])
			if _, ok := allowedPlaceholders[placeholder]; !ok {
				return fmt.Errorf("custom header %q uses unknown placeholder %q", name, match[0])
			}
			if match[1] != placeholder {
				return fmt.Errorf("custom header %q uses malformed placeholder %q", name, match[0])
			}
		}
		remaining := placeholderPattern.ReplaceAllString(value, "")
		if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
			return fmt.Errorf("custom header %q contains a malformed placeholder", name)
		}
	}
	return nil
}

// Apply 展开动态变量并覆盖请求中同名的默认请求头。
// Apply expands dynamic values and overrides same-name default request headers.
func Apply(ctx context.Context, destination http.Header, templates map[string]string) error {
	if len(templates) == 0 {
		return nil
	}
	if err := ValidateTemplates(templates); err != nil {
		return err
	}
	values := dynamicValues(ctx, time.Now())
	for name, template := range templates {
		expanded := placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
			parts := placeholderPattern.FindStringSubmatch(match)
			return values[parts[1]]
		})
		destination.Set(name, expanded)
	}
	return nil
}

func dynamicValues(ctx context.Context, now time.Time) map[string]string {
	metadata, _ := ctx.Value(contextKey{}).(requestContext)
	requestUUID := uuid.NewString()
	return map[string]string{
		"conversation_id": metadata.conversationID,
		"session_id":      metadata.conversationID,
		"message_id":      metadata.messageID,
		"trace_id":        trace.FromContext(ctx),
		"uuid":            requestUUID,
		"snowflake_id":    nextSnowflakeID(now),
		"timestamp":       strconv.FormatInt(now.Unix(), 10),
		"timestamp_ms":    strconv.FormatInt(now.UnixMilli(), 10),
	}
}

func validHeaderName(name string) bool {
	for i := 0; i < len(name); i++ {
		character := name[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		character := value[i]
		if character == '\t' || (character >= 0x20 && character != 0x7f) {
			continue
		}
		return false
	}
	return true
}

func snowflakeNodeID() uint64 {
	hasher := fnv.New32a()
	hostname, _ := os.Hostname()
	_, _ = hasher.Write([]byte(hostname))
	_, _ = hasher.Write([]byte(strconv.Itoa(os.Getpid())))
	return uint64(hasher.Sum32()) & snowflakeNodeMask
}

func nextSnowflakeID(now time.Time) string {
	// 采用 2024-01-01 UTC 作为自定义纪元，保留完整的 41 位时间范围。
	// Use a recent custom epoch to retain the full 41-bit time range.
	const epochMillis int64 = 1704067200000
	millis := now.UnixMilli() - epochMillis
	if millis < 0 {
		millis = 0
	}

	snowflakeState.Lock()
	defer snowflakeState.Unlock()
	if millis < snowflakeState.lastMillis {
		millis = snowflakeState.lastMillis
	}
	if millis == snowflakeState.lastMillis {
		snowflakeState.sequence = (snowflakeState.sequence + 1) & snowflakeSequenceMask
		if snowflakeState.sequence == 0 {
			millis++
		}
	} else {
		snowflakeState.sequence = 0
	}
	snowflakeState.lastMillis = millis

	id := (uint64(millis) << 22) |
		((snowflakeState.node & snowflakeNodeMask) << 12) |
		uint64(snowflakeState.sequence)
	return strconv.FormatUint(id, 10)
}
