package config

import (
	"os"
	"strings"
	"testing"
)

// TestStripTrailingCommas covers the four shapes the
// tolerance pass needs to handle, plus one negative case
// (commas inside strings must be left alone).
func TestStripTrailingCommas(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"object trailing comma",
			`{"a":1,}`,
			`{"a":1}`,
		},
		{
			"array trailing comma",
			`[1,2,3,]`,
			`[1,2,3]`,
		},
		{
			"nested trailing commas",
			`{"a":{"b":1,},"c":[1,],}`,
			`{"a":{"b":1},"c":[1]}`,
		},
		{
			"whitespace before brace",
			// The pass drops only the comma; whitespace
			// before the closing brace is preserved so the
			// file looks as the user typed it (minus the
			// bad comma).
			`{"a":1   ,   }`,
			`{"a":1      }`,
		},
		{
			"no trailing comma",
			`{"a":1,"b":2}`,
			`{"a":1,"b":2}`,
		},
		{
			"comma inside string preserved",
			`{"msg":"hi, world",}`,
			`{"msg":"hi, world"}`,
		},
		{
			"escaped quote inside string preserved",
			`{"msg":"she said \"hi, there\", ok",}`,
			`{"msg":"she said \"hi, there\", ok"}`,
		},
		{
			"comma inside array of strings",
			`["a","b,","c",]`,
			`["a","b,","c"]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stripTrailingCommas([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("stripTrailingCommas(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTryUnmarshalWithTolerance_StripsTrailingComma(t *testing.T) {
	// The exact shape that bit us in production: hand-edited
	// config with a stray comma between the last field and
	// the closing brace.
	corrupt := `{"server":{"host":"127.0.0.1","port":8960},}`
	var got Config
	cleaned, tolerated, err := tryUnmarshalWithTolerance([]byte(corrupt), &got)
	if err != nil {
		t.Fatalf("expected tolerance fallback to succeed, got: %v", err)
	}
	if !tolerated {
		t.Error("expected tolerated=true")
	}
	if !strings.Contains(string(cleaned), `"port":8960`) {
		t.Errorf("cleaned bytes should still contain the port: %s", cleaned)
	}
	if got.Server.Port != 8960 {
		t.Errorf("Server.Port = %d, want 8960", got.Server.Port)
	}
}

func TestTryUnmarshalWithTolerance_PreservesCleanJSON(t *testing.T) {
	// Strict-valid JSON should not be rewritten.
	clean := `{"server":{"host":"127.0.0.1","port":8960}}`
	var got Config
	_, tolerated, err := tryUnmarshalWithTolerance([]byte(clean), &got)
	if err != nil {
		t.Fatalf("clean JSON should parse on first attempt: %v", err)
	}
	if tolerated {
		t.Error("clean JSON should not need tolerance fallback")
	}
}

func TestTryUnmarshalWithTolerance_RejectsBadJSON(t *testing.T) {
	// Real corruption (not just trailing comma) should still
	// fail loudly — the fallback is narrow on purpose.
	bad := `{"a": 1, "b": @}`
	var got Config
	_, _, err := tryUnmarshalWithTolerance([]byte(bad), &got)
	if err == nil {
		t.Fatal("expected error for truly malformed JSON")
	}
}

// TestLoad_RecoversFromTrailingComma is the integration test:
// write a config with a trailing comma, call Load, verify it
// succeeds and the file is rewritten cleanly. The original
// corruption mode we hit in production was exactly this.
func TestLoad_RecoversFromTrailingComma(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	corrupt := `{
  "server": {"host": "127.0.0.1", "port": 8960},
  "style": {"default": "tech"},
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", corrupt); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with trailing comma should succeed, got: %v", err)
	}
	if cfg.Style.Default != "tech" {
		t.Errorf("Style.Default = %q, want tech", cfg.Style.Default)
	}

	// File should have been rewritten to clean JSON.
	data, _ := os.ReadFile(dir + "/.p-chat/config.json")
	if strings.Contains(string(data), ",\n}") || strings.Contains(string(data), ", }") {
		t.Errorf("file not cleaned after Load: %s", data)
	}
}
