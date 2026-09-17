package runtimeprofile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// InstanceEnv carries the parent-selected process instance identity.
	InstanceEnv = "PCHAT_INSTANCE_ID"
	// RuntimeFileEnv tells pchat-server where to publish its bound address.
	RuntimeFileEnv = "PCHAT_RUNTIME_FILE"
)

// Announcement is the startup handshake published after pchat-server owns its
// listener and before it begins serving requests.
type Announcement struct {
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	InstanceID  string `json:"instance_id"`
	PID         int    `json:"pid"`
	Address     string `json:"address"`
	BaseURL     string `json:"base_url"`
}

// NewInstanceID returns a random process instance identifier.
func NewInstanceID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("runtime profile: create instance id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// NewAnnouncementPath reserves a unique temporary pathname for a single
// parent/child startup handshake. The placeholder is removed before return so
// the server can publish the announcement atomically with a rename.
func NewAnnouncementPath() (string, error) {
	file, err := os.CreateTemp("", "pchat-runtime-*.json")
	if err != nil {
		return "", fmt.Errorf("runtime profile: create announcement path: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("runtime profile: close announcement placeholder: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("runtime profile: prepare announcement path: %w", err)
	}
	return path, nil
}

// WriteAnnouncement atomically publishes a server startup announcement.
func WriteAnnouncement(path string, announcement Announcement) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("runtime profile: announcement path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("runtime profile: create announcement directory: %w", err)
	}
	payload, err := json.Marshal(announcement)
	if err != nil {
		return fmt.Errorf("runtime profile: encode announcement: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pchat-runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("runtime profile: create announcement temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("runtime profile: protect announcement temp file: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("runtime profile: write announcement: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("runtime profile: close announcement: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("runtime profile: publish announcement: %w", err)
	}
	return nil
}

// ReadAnnouncement reads a server startup announcement.
func ReadAnnouncement(path string) (Announcement, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Announcement{}, fmt.Errorf("runtime profile: read announcement: %w", err)
	}
	var announcement Announcement
	if err := json.Unmarshal(payload, &announcement); err != nil {
		return Announcement{}, fmt.Errorf("runtime profile: decode announcement: %w", err)
	}
	return announcement, nil
}

// Validate verifies that an announcement belongs to the expected profile and
// process, and returns its bound TCP port.
func (a Announcement) Validate(profile Profile, instanceID string, pid int) (int, error) {
	if a.ProfileID != profile.ID || a.InstanceID != instanceID {
		return 0, fmt.Errorf("runtime profile: announcement identity mismatch: got profile=%q instance=%q, want profile=%q instance=%q",
			a.ProfileID, a.InstanceID, profile.ID, instanceID)
	}
	if pid > 0 && a.PID != pid {
		return 0, fmt.Errorf("runtime profile: announcement pid=%d, want %d", a.PID, pid)
	}
	_, portText, err := net.SplitHostPort(a.Address)
	if err != nil {
		return 0, fmt.Errorf("runtime profile: invalid announcement address %q: %w", a.Address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("runtime profile: invalid announcement port %q", portText)
	}
	baseURL, err := url.Parse(a.BaseURL)
	if err != nil || baseURL.Scheme != "http" || baseURL.Hostname() == "" || baseURL.Port() != portText {
		return 0, fmt.Errorf("runtime profile: invalid announcement base URL %q", a.BaseURL)
	}
	return port, nil
}

// WaitAnnouncement waits until the expected child atomically publishes a
// valid startup announcement or the context/timeout expires.
func WaitAnnouncement(ctx context.Context, path string, profile Profile, instanceID string, pid int, timeout time.Duration) (Announcement, int, error) {
	deadline := time.Now().Add(timeout)
	for {
		announcement, err := ReadAnnouncement(path)
		if err == nil {
			port, validateErr := announcement.Validate(profile, instanceID, pid)
			if validateErr != nil {
				return Announcement{}, 0, validateErr
			}
			return announcement, port, nil
		}
		if time.Now().After(deadline) {
			return Announcement{}, 0, fmt.Errorf("runtime profile: server did not announce its address within %v", timeout)
		}
		select {
		case <-ctx.Done():
			return Announcement{}, 0, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
