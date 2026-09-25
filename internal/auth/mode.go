// mode.go — 兼容迁移模式（决策票 #6）：健康广告。
// Compatibility migration mode (ticket #6): health advertisement.
//
// fail closed 立即生效后，server 总是持有实例凭据（父进程经
// PCHAT_INSTANCE_CAPABILITY 下发，或独立启动时自铸），不存在 legacy-open
// 过渡窗。/api/v1/health 广告 auth_required，让浏览器端 JS 能在首个业务
// 请求之前就主动展示引导页，而不是等一个 401。
//
// With fail closed taking effect immediately, the server always holds an
// instance capability (handed down by the parent via
// PCHAT_INSTANCE_CAPABILITY, or self-minted when standalone); there is no
// legacy-open transition window. /api/v1/health advertises auth_required so
// browser JS can proactively show the unlock page before the first business
// request instead of waiting for a 401.
package auth

import "github.com/gin-gonic/gin"

// HealthAuthRequiredField 是 /api/v1/health 响应中广告鉴权要求的字段名。
// HealthAuthRequiredField is the /api/v1/health response field advertising
// the authentication requirement.
const HealthAuthRequiredField = "auth_required"

// HealthFields 返回合并进 /health 响应的鉴权广告字段。server 持有有效实例
// 凭据时恒为 true；零值凭据（理论上的无鉴权调试形态）广告 false。
// HealthFields returns the auth advertisement fields merged into the /health
// response. It is true whenever the server holds a valid instance capability;
// a zero capability (the theoretical no-auth debug shape) advertises false.
func HealthFields(capability Capability) gin.H {
	return gin.H{HealthAuthRequiredField: !capability.IsZero()}
}
