package im

import "testing"

func TestBuildSessionKeyHonorsScope(t *testing.T) {
	ev := IMEvent{
		Platform: "feishu",
		Chat:     ChatRef{Platform: "feishu", ChatID: "oc_group", ChatType: "group", ThreadID: "om_root"},
		Sender:   SenderRef{ID: "ou_sender"},
	}

	tests := []struct {
		scope string
		want  string
	}{
		{scope: "per_thread", want: "im:feishu:g:oc_group:t:om_root"},
		{scope: "per_chat", want: "im:feishu:g:oc_group"},
		{scope: "per_sender", want: "im:feishu:u:ou_sender"},
		{scope: "bad", want: "im:feishu:g:oc_group:t:om_root"},
	}
	for _, tt := range tests {
		if got := BuildSessionKey(ev, tt.scope); got != tt.want {
			t.Fatalf("BuildSessionKey(%q) = %q, want %q", tt.scope, got, tt.want)
		}
	}
}

func TestBuildSessionKeyPrivateFallsBackToSender(t *testing.T) {
	ev := IMEvent{
		Platform: "wechat",
		Chat:     ChatRef{Platform: "wechat", ChatID: "user-chat", ChatType: "private"},
		Sender:   SenderRef{ID: "user-1"},
	}
	if got := BuildSessionKey(ev, "per_thread"); got != "im:wechat:u:user-1" {
		t.Fatalf("private per_thread key = %q, want sender key", got)
	}
}
