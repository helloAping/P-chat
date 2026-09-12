package llm

import "strings"

// RuntimeContextPrefix 标识应用追加的上下文，保留其在历史中的位置。
// RuntimeContextPrefix identifies application context whose history position is preserved.
const RuntimeContextPrefix = "pchat_context_"

// IsRuntimeContext 判断消息是否为应用生成的增量上下文。
// IsRuntimeContext reports whether a message is application-generated incremental context.
func IsRuntimeContext(msg ChatMessage) bool {
	return msg.Role == RoleSystem && strings.HasPrefix(msg.Name, RuntimeContextPrefix)
}

func runtimeContextContent(msg ChatMessage) string {
	return "<application_context name=\"" + msg.Name + "\">\n" + msg.Content + "\n</application_context>"
}
