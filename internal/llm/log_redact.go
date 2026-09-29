package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const diagnosticRedacted = "[REDACTED]"

var diagnosticSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]+`),
	// Header: value and key=value forms. The key may have a vendor prefix
	// (for example X-Goog-Api-Key), but must end in a credential-bearing name.
	regexp.MustCompile(`(?i)((?:^|[\s,;{&])(?:"|')?(?:(?:[a-z0-9_.-]*)(?:api[_-]?key|token|secret|password|cookie|authorization)|apikey|passwd|pchat[_-]?instance[_-]?capability)(?:"|')?\s*[:=]\s*)(?:"(?:\\.|[^"])*"|'(?:\\.|[^'])*'|(?:bearer|basic)\s+[^\s,;}&]+|[^\s,;}&]+)`),
	regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|key|secret|password)=)[^&#\s]+`),
}

func llmDebugEnabled() bool {
	return os.Getenv("PC_LLM_DEBUG") == "1"
}

// redactDiagnosticText 对日志或上游错误中的凭据做统一脱敏；即使开启调试也不会输出凭据。
// redactDiagnosticText centrally removes credentials from logs and upstream errors, including in debug mode.
func redactDiagnosticText(raw string) string {
	raw = strings.ToValidUTF8(raw, "�")
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		redactDiagnosticValue(value)
		if encoded, err := json.Marshal(value); err == nil {
			raw = string(encoded)
		}
	}
	for _, pattern := range diagnosticSecretPatterns {
		raw = pattern.ReplaceAllString(raw, `${1}`+diagnosticRedacted)
	}
	return raw
}

func redactDiagnosticValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isSensitiveDiagnosticKey(key) {
				typed[key] = diagnosticRedacted
				continue
			}
			redactDiagnosticValue(child)
		}
	case []any:
		for _, child := range typed {
			redactDiagnosticValue(child)
		}
	}
}

func isSensitiveDiagnosticKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(normalized)
	switch normalized {
	case "authorization", "proxy_authorization", "api_key", "apikey", "x_api_key",
		"token", "access_token", "refresh_token", "id_token", "client_secret",
		"secret", "password", "passwd", "cookie", "set_cookie",
		"pchat_instance_capability":
		return true
	}
	return strings.HasSuffix(normalized, "_api_key") ||
		strings.HasSuffix(normalized, "_token") ||
		strings.HasSuffix(normalized, "_secret") ||
		strings.HasSuffix(normalized, "_password") ||
		strings.HasSuffix(normalized, "_cookie") ||
		strings.HasSuffix(normalized, "_authorization")
}

func redactedPayloadPreview(payload []byte, max int) string {
	redacted := redactDiagnosticText(string(payload))
	if max > 0 && len(redacted) > max {
		redacted = strings.ToValidUTF8(redacted[:max], "�") + "...(truncated)"
	}
	// 用引号转义换行，避免恶意上游伪造新的日志行。
	// Quote newlines so a malicious upstream cannot forge additional log lines.
	return strconv.Quote(redacted)
}

func sanitizeProviderDetail(detail string, max int) string {
	redacted := strings.TrimSpace(redactDiagnosticText(detail))
	if max > 0 && len(redacted) > max {
		redacted = strings.ToValidUTF8(redacted[:max], "�") + "...(truncated)"
	}
	return redacted
}

func payloadFingerprint(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:6])
}

func logLLMDebugPayload(scope string, sequence int, payload []byte) {
	if !llmDebugEnabled() {
		return
	}
	log.Printf("%s debug payload #%d: bytes=%d sha256=%s preview=%s",
		scope, sequence, len(payload), payloadFingerprint(payload), redactedPayloadPreview(payload, 2048))
}

func logMalformedSSE(scope string, sequence, failures int, payload []byte, parseErr error) {
	if failures > 3 {
		return
	}
	// 默认只记录不可逆摘要和长度，不把消息正文写入日志。
	// Default logs contain only an irreversible digest and size, never message text.
	log.Printf("%s malformed SSE #%d: %v bytes=%d sha256=%s",
		scope, sequence, parseErr, len(payload), payloadFingerprint(payload))
	logLLMDebugPayload(scope+" malformed SSE", sequence, payload)
}

func logProxySSE(scope string, sequence int, payload []byte) {
	// proxy error 也可能回显请求、Authorization 或 API key，因此默认只记元数据。
	// Proxy errors may echo requests or credentials, so default logs keep metadata only.
	log.Printf("%s proxy error chunk #%d: bytes=%d sha256=%s",
		scope, sequence, len(payload), payloadFingerprint(payload))
	logLLMDebugPayload(scope+" proxy error", sequence, payload)
}

// upstreamHTTPError 保留状态码用于错误分类，但默认错误文本不携带上游响应正文。
// upstreamHTTPError retains the status for classification while omitting the upstream body by default.
func upstreamHTTPError(protocol string, status int, body []byte) error {
	if llmDebugEnabled() {
		log.Printf("[llm/%s] upstream HTTP error status=%d bytes=%d sha256=%s preview=%s",
			protocol, status, len(body), payloadFingerprint(body), redactedPayloadPreview(body, 2048))
	}
	summary := fmt.Sprintf("response body omitted (bytes=%d sha256=%s)", len(body), payloadFingerprint(body))
	if IsVisionUnsupportedError(fmt.Errorf("%s", string(body))) {
		// 保留无正文的稳定标记，让既有分类器仍能识别视觉能力错误。
		// Keep a body-free marker so the existing classifier still detects vision capability errors.
		summary += "; model does not support image input"
	}
	return fmt.Errorf("%s http %d: %s", protocol, status, summary)
}
