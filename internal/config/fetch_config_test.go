package config

import "testing"

func TestUpdateFetchConfig_ProviderRules(t *testing.T) {
	_, _ = withTempProject(t)

	if _, err := UpdateFetchConfig(FetchConfigPatch{Provider: ptrStr("bing")}); err == nil {
		t.Fatal("unknown provider should fail")
	}
	updated, err := UpdateFetchConfig(FetchConfigPatch{Provider: ptrStr("local")})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Provider != "local" {
		t.Fatalf("provider = %q", updated.Provider)
	}
	if _, err := UpdateFetchConfig(FetchConfigPatch{Provider: ptrStr("deepseek")}); err == nil {
		t.Fatal("deepseek without key should fail")
	}
	key := "sk-test"
	updated, err = UpdateFetchConfig(FetchConfigPatch{Provider: ptrStr("deepseek"), APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Provider != "deepseek" || updated.APIKey != key {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := UpdateFetchConfig(FetchConfigPatch{Provider: ptrStr("qwen"), APIKey: &key}); err == nil {
		t.Fatal("qwen without base_url should fail")
	}
}
