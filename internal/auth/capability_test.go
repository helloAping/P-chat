package auth

import (
	"strings"
	"testing"
)

// TestNewCapabilityFormat 验证凭据是 64 字符 hex（256 位熵）且两次生成不同。
// TestNewCapabilityFormat verifies the capability is 64 hex chars (256 bits
// of entropy) and two mints differ.
func TestNewCapabilityFormat(t *testing.T) {
	first, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	second, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	if got := len(first.String()); got != capabilityBytes*2 {
		t.Fatalf("capability length = %d, want %d", got, capabilityBytes*2)
	}
	if first.String() == second.String() {
		t.Fatal("two minted capabilities must differ")
	}
	if first.IsZero() {
		t.Fatal("minted capability must not be zero")
	}
}

// TestCapabilityFromStringRejectsEmpty 验证空凭据被拒绝。
// TestCapabilityFromStringRejectsEmpty verifies empty capabilities are
// rejected.
func TestCapabilityFromStringRejectsEmpty(t *testing.T) {
	if _, err := CapabilityFromString(""); err == nil {
		t.Fatal("empty capability must be rejected")
	}
	var zero Capability
	if !zero.IsZero() {
		t.Fatal("zero Capability must report IsZero")
	}
}

// TestCapabilityVerify 验证常数时间比较的接受/拒绝路径。
// TestCapabilityVerify covers the constant-time compare accept/reject paths.
func TestCapabilityVerify(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	if !cap.Verify(cap.String()) {
		t.Fatal("capability must verify against itself")
	}
	if cap.Verify(strings.Repeat("0", len(cap.String()))) {
		t.Fatal("wrong capability of equal length must fail")
	}
	if cap.Verify("short") {
		t.Fatal("wrong capability of different length must fail")
	}
	if cap.Verify("") {
		t.Fatal("empty candidate must fail")
	}
	var zero Capability
	if zero.Verify(zero.String()) {
		t.Fatal("zero capability must never verify")
	}
}

// TestResolveCapabilityPrefersEnv 验证 spawn 场景下父进程下发的环境变量优先。
// TestResolveCapabilityPrefersEnv verifies the parent-provided environment
// variable wins in the spawn case.
func TestResolveCapabilityPrefersEnv(t *testing.T) {
	t.Setenv(CapabilityEnv, "parent-minted-capability")
	cap, err := ResolveCapability()
	if err != nil {
		t.Fatalf("ResolveCapability: %v", err)
	}
	if cap.String() != "parent-minted-capability" {
		t.Fatalf("capability = %q, want parent-provided value", cap.String())
	}
}

// TestResolveCapabilitySelfMint 验证独立 server 场景下自行生成，且两次独立
// 解析得到不同凭据（重启即换新）。
// TestResolveCapabilitySelfMint verifies a standalone server mints its own,
// and two independent resolves differ (regenerated on restart).
func TestResolveCapabilitySelfMint(t *testing.T) {
	t.Setenv(CapabilityEnv, "")
	first, err := ResolveCapability()
	if err != nil {
		t.Fatalf("ResolveCapability: %v", err)
	}
	second, err := ResolveCapability()
	if err != nil {
		t.Fatalf("ResolveCapability: %v", err)
	}
	if first.IsZero() || second.IsZero() {
		t.Fatal("self-minted capabilities must be non-zero")
	}
	if first.String() == second.String() {
		t.Fatal("self-minted capabilities must differ across resolves")
	}
}
