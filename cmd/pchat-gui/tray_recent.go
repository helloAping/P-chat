package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	trayRecentSessionLimit = 5
	traySessionTitleLimit  = 44
)

type traySession struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ProjectPath string `json:"project_path"`
	UpdatedAt   int64  `json:"updated_at"`
}

type traySessionListResponse struct {
	Sessions []traySession `json:"sessions"`
}

// recentTraySessions 为托盘菜单读取少量最近会话；失败时返回空列表，不影响菜单打开。
// recentTraySessions reads a small, global recent-session list for the tray menu.
func (a *App) recentTraySessions() []traySession {
	backend := a.GetBackendURL()
	if backend == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(backend, "/")+"/api/v1/sessions", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 600 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var body traySessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	return limitTraySessions(body.Sessions, trayRecentSessionLimit)
}

func limitTraySessions(sessions []traySession, limit int) []traySession {
	if limit <= 0 || len(sessions) == 0 {
		return nil
	}
	out := make([]traySession, 0, minInt(len(sessions), limit))
	for _, session := range sessions {
		if strings.TrimSpace(session.ID) == "" {
			continue
		}
		out = append(out, session)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func traySessionMenuLabel(index int, session traySession) string {
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = session.ID
	}
	title = truncateRunes(title, traySessionTitleLimit)
	// 转义 Win32 菜单快捷键标记，避免标题里的 & 被当成助记键。
	// Escape Win32 menu accelerators so user titles containing "&" render literally.
	title = strings.ReplaceAll(title, "&", "&&")
	return fmt.Sprintf("%d. %s", index+1, title)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= max-1 {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String() + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
