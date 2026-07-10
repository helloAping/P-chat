package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// withTestOverrides sets a fresh fake executable path +
// clears the home override for the duration of the test,
// then restores both via t.Cleanup. We also drop the
// cache explicitly because SetExecutableForTest /
// SetHomeForTest are no-op if the cache was already
// populated by an earlier test.
func withTestOverrides(t *testing.T, exe, home string) {
	t.Helper()
	if exe != "" {
		SetExecutableForTest(exe)
	} else {
		SetExecutableForTest("")
	}
	if home != "" {
		SetHomeForTest(home)
	} else {
		SetHomeForTest("")
	}
	t.Cleanup(func() {
		SetExecutableForTest("")
		SetHomeForTest("")
	})
}

func TestResolveHome_EnvVar(t *testing.T) {
	tmp := t.TempDir()
	withTestOverrides(t, "", tmp)
	got := GlobalDir()
	if got != tmp {
		t.Errorf("GlobalDir() = %q, want %q", got, tmp)
	}
	if s := ResolveStrategy(); s != StrategyEnvVar {
		t.Errorf("strategy = %q, want %q", s, StrategyEnvVar)
	}
}

func TestResolveHome_SiblingBin(t *testing.T) {
	tmp := t.TempDir()
	withTestOverrides(t, filepath.Join(tmp, "bin", "pchat-server.exe"), "")
	got := GlobalDir()
	want := filepath.Join(tmp, "bin", GlobalDirName)
	if got != want {
		t.Errorf("GlobalDir() = %q, want %q", got, want)
	}
	if s := ResolveStrategy(); s != StrategyDevBin {
		t.Errorf("strategy = %q, want %q", s, StrategyDevBin)
	}
}

func TestResolveHome_SiblingDevBin(t *testing.T) {
	tmp := t.TempDir()
	withTestOverrides(t, filepath.Join(tmp, "dev-bin", "pchat.exe"), "")
	got := GlobalDir()
	want := filepath.Join(tmp, "dev-bin", GlobalDirName)
	if got != want {
		t.Errorf("GlobalDir() = %q, want %q", got, want)
	}
	if s := ResolveStrategy(); s != StrategyDevBin {
		t.Errorf("strategy = %q, want %q", s, StrategyDevBin)
	}
}

func TestResolveHome_EnvVarBeatsSibling(t *testing.T) {
	tmp := t.TempDir()
	// Both env-var override AND a bin/-style exec path set.
	// PCHAT_HOME must win.
	withTestOverrides(t, filepath.Join(tmp, "bin", "pchat.exe"), tmp)
	if s := ResolveStrategy(); s != StrategyEnvVar {
		t.Errorf("strategy = %q, want %q", s, StrategyEnvVar)
	}
}

func TestResolveHome_NonBinSiblingFallsThrough(t *testing.T) {
	// Binary lives in a non-bin folder (e.g. C:\Program
	// Files\pchat\). No isolation — fall back to $HOME.
	tmp := t.TempDir()
	withTestOverrides(t, filepath.Join(tmp, "Program Files", "pchat", "pchat.exe"), "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, GlobalDirName)
	if got := GlobalDir(); got != want {
		t.Errorf("GlobalDir() = %q, want %q", got, want)
	}
	if s := ResolveStrategy(); s != StrategyHome {
		t.Errorf("strategy = %q, want %q", s, StrategyHome)
	}
}

func TestResolveHome_EmptyExecFallsThrough(t *testing.T) {
	// Defensive: if os.Executable fails (sandbox / chroot)
	// we must still return a usable path, not panic.
	withTestOverrides(t, "", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, GlobalDirName)
	if got := GlobalDir(); got != want {
		t.Errorf("GlobalDir() = %q, want %q", got, want)
	}
}

func TestEnsureGlobal_UsesSiblingDir(t *testing.T) {
	// Integration: build a sibling-style layout in a temp
	// dir, call EnsureGlobal, verify all the expected
	// subdirs were created under the SIBLING .p-chat —
	// NOT under the user's real $HOME.
	//
	// We also redirect $USERPROFILE to a temp dir so the
	// $HOME fallback would be a clean test target rather
	// than the operator's real ~/.p-chat. The test asserts
	// the SIBLING was chosen, not the $HOME fallback.
	tmp := t.TempDir()
	homeTmp := t.TempDir()
	t.Setenv("USERPROFILE", homeTmp)
	t.Setenv("HOME", homeTmp)

	withTestOverrides(t, filepath.Join(tmp, "dev-bin", "pchat-server.exe"), "")
	if err := EnsureGlobal(); err != nil {
		t.Fatal(err)
	}

	// The sibling .p-chat should be populated.
	expected := []string{
		GlobalDir(),        // tmp/dev-bin/.p-chat
		MemoryDir(),        // tmp/dev-bin/.p-chat/memory
		GlobalSkillsDir(),  // tmp/dev-bin/.p-chat/skills
		GlobalRulesDir(),   // tmp/dev-bin/.p-chat/rules
		GlobalPromptsDir(), // tmp/dev-bin/.p-chat/prompts
		ToolsDir(),         // tmp/dev-bin/.p-chat/tools
		KnowledgeDir(),     // tmp/dev-bin/.p-chat/knowledge
		UploadsDir(),       // tmp/dev-bin/.p-chat/uploads
	}
	for _, d := range expected {
		fi, err := os.Stat(d)
		if err != nil {
			t.Errorf("missing dir %s: %v", d, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}

	// Critical: the $HOME fallback .p-chat must NOT have
	// been touched (only the sibling should be).
	homePath := filepath.Join(homeTmp, GlobalDirName)
	if _, err := os.Stat(homePath); err == nil {
		t.Errorf("HOME fallback was touched: %s exists", homePath)
	}
}
