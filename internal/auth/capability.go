// capability.go — 实例凭据（Instance Capability）原型（决策票 #8）。
// Instance capability prototype (decision ticket #8).
//
// 设计要点（与 docs/plans/runtime-trust-decision-map.md #2/#8 对齐）：
//   - 实例凭据是 Runtime Instance 级短期 capability：随进程实例生灭，
//     重启即换新，不持久化；撤销语义 = 进程退出。
//   - 分发零配置：spawn 场景由父进程（pchat / pchat web / pchat-gui）
//     经环境变量 PCHAT_INSTANCE_CAPABILITY 下发（与 PCHAT_INSTANCE_ID
//     同通道）；独立启动的 pchat-server 自行生成。
//   - 凭据不得出现在任何 HTTP 响应体、URL 或日志中；校验使用常数时间比较。
//   - 本包只提供凭据原语与中间件；接入生产路由属于 #7 的实现切片。
//
// Design notes (aligned with docs/plans/runtime-trust-decision-map.md #2/#8):
//   - The instance capability is a Runtime Instance scoped short-lived
//     credential: it is born and dies with the process, is regenerated on
//     restart, never persisted; revocation == process exit.
//   - Zero-config distribution: a spawning parent (pchat / pchat web /
//     pchat-gui) passes it via the PCHAT_INSTANCE_CAPABILITY environment
//     variable (same channel as PCHAT_INSTANCE_ID today); a standalone
//     pchat-server mints its own.
//   - The capability must never appear in any HTTP response body, URL or
//     log line; verification uses constant-time comparison.
//   - This package provides only the credential primitives and middleware;
//     wiring into production routes is a #7 slice.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
)

// CapabilityEnv 是父进程向 pchat-server 下发实例凭据的环境变量。
// CapabilityEnv is the environment variable a parent process uses to hand
// the instance capability to pchat-server.
const CapabilityEnv = "PCHAT_INSTANCE_CAPABILITY"

// capabilityBytes 是凭据的随机熵长度（hex 编码后 64 字符）。
// capabilityBytes is the random entropy size of a capability (64 hex chars
// once encoded).
const capabilityBytes = 32

// Capability 是一个 Runtime Instance 的短期凭据。值不可变；比较走常数时间。
// Capability is a Runtime Instance's short-lived credential. The value is
// immutable; comparison is constant-time.
type Capability struct {
	value string
}

// NewCapability 生成 256 位随机实例凭据。
// NewCapability mints a 256-bit random instance capability.
func NewCapability() (Capability, error) {
	buf := make([]byte, capabilityBytes)
	if _, err := rand.Read(buf); err != nil {
		return Capability{}, fmt.Errorf("auth: mint instance capability: %w", err)
	}
	return Capability{value: hex.EncodeToString(buf)}, nil
}

// CapabilityFromString 包装一个外部来源的凭据（环境变量、用户粘贴）。
// 空值被拒绝；不限制字符集，常数时间比较对任意字节串都成立。
// CapabilityFromString wraps an externally sourced capability (environment
// variable, user paste). Empty values are rejected; the charset is not
// restricted because constant-time comparison works for any byte string.
func CapabilityFromString(value string) (Capability, error) {
	if value == "" {
		return Capability{}, fmt.Errorf("auth: instance capability is empty")
	}
	return Capability{value: value}, nil
}

// ResolveCapability 解析 server 启动时的实例凭据：父进程经环境变量下发时
// 采用该值（spawn 场景）；否则自行生成（独立 pchat-server 场景）。
// ResolveCapability resolves the instance capability at server startup: the
// parent-provided environment value wins (spawn case); otherwise a fresh one
// is minted (standalone pchat-server case).
func ResolveCapability() (Capability, error) {
	if env := os.Getenv(CapabilityEnv); env != "" {
		return CapabilityFromString(env)
	}
	return NewCapability()
}

// String 返回凭据明文。仅用于：父进程写环境变量、server 设置 HttpOnly
// cookie、测试断言。禁止写入日志、URL 或 HTTP 响应体。
// String returns the capability plaintext. Only for: a parent writing the
// environment variable, the server setting the HttpOnly cookie, and test
// assertions. Never log it, put it in a URL, or return it in a body.
func (c Capability) String() string {
	return c.value
}

// IsZero 报告凭据是否未初始化。
// IsZero reports whether the capability is uninitialized.
func (c Capability) IsZero() bool {
	return c.value == ""
}

// Verify 以常数时间比较候选凭据。空候选恒失败。
// Verify constant-time-compares a candidate credential. Empty candidates
// always fail.
func (c Capability) Verify(candidate string) bool {
	if c.value == "" || candidate == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.value), []byte(candidate)) == 1
}
