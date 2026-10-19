package server

import "testing"

func TestLegacyIMConversationResponseTitle(t *testing.T) {
	tests := []struct {
		name, id, title, want string
	}{
		{"ordinary session", "local-1", "Project", "Project"},
		{"old raw title", "im:wechat:u:alice", "im:wechat:u:alice", "微信 · alice"},
		{"old sender id", "im:feishu:u:alice", "alice", "飞书 · alice"},
		{"user title", "im:wechat:u:alice", "客户 A", "客户 A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := legacyIMConversationResponseTitle(tt.id, tt.title); got != tt.want {
				t.Fatalf("title = %q, want %q", got, tt.want)
			}
		})
	}
}
