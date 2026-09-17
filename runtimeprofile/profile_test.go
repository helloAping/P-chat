package runtimeprofile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveUsesDataHomeAsStableProfileIdentity(t *testing.T) {
	root := t.TempDir()
	dataHome := filepath.Join(root, "dev-bin", ".p-chat")

	first, err := Resolve(dataHome, "dev")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(filepath.Join(root, "dev-bin", "..", "dev-bin", ".p-chat"), "renamed")
	if err != nil {
		t.Fatal(err)
	}

	if first.ID == "" {
		t.Fatal("profile ID must not be empty")
	}
	if first.ID != second.ID {
		t.Fatalf("same data home produced different IDs: %q != %q", first.ID, second.ID)
	}
	if first.DataHome != second.DataHome {
		t.Fatalf("same data home was not canonicalized consistently: %q != %q", first.DataHome, second.DataHome)
	}
	if first.Name != "dev" || second.Name != "renamed" {
		t.Fatalf("display names should not affect identity: first=%q second=%q", first.Name, second.Name)
	}
}

func TestResolveSeparatesDifferentDataHomes(t *testing.T) {
	root := t.TempDir()
	prod, err := Resolve(filepath.Join(root, "prod", ".p-chat"), "prod")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := Resolve(filepath.Join(root, "dev", ".p-chat"), "dev")
	if err != nil {
		t.Fatal(err)
	}

	if prod.ID == dev.ID {
		t.Fatalf("different data homes share profile ID %q", prod.ID)
	}
	if prod.WindowTitle() != "P-Chat" {
		t.Fatalf("prod title = %q, want P-Chat", prod.WindowTitle())
	}
	if dev.WindowTitle() != "P-Chat [dev]" {
		t.Fatalf("dev title = %q, want P-Chat [dev]", dev.WindowTitle())
	}
}

func TestAnnouncementRoundTripsThroughRuntimeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	want := Announcement{
		ProfileID:   "profile-1",
		ProfileName: "dev",
		InstanceID:  "instance-1",
		PID:         os.Getpid(),
		Address:     "127.0.0.1:43210",
		BaseURL:     "http://127.0.0.1:43210",
	}
	if err := WriteAnnouncement(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAnnouncement(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("announcement = %#v, want %#v", got, want)
	}
}

func TestWaitAnnouncementRejectsAnotherProcessIdentity(t *testing.T) {
	profile, err := Resolve(filepath.Join(t.TempDir(), ".p-chat"), "test")
	if err != nil {
		t.Fatal(err)
	}
	path, err := NewAnnouncementPath()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if err := WriteAnnouncement(path, Announcement{
		ProfileID:  profile.ID,
		InstanceID: "another-instance",
		PID:        os.Getpid(),
		Address:    "127.0.0.1:43210",
		BaseURL:    "http://127.0.0.1:43210",
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err = WaitAnnouncement(context.Background(), path, profile, "expected-instance", os.Getpid(), time.Second)
	if err == nil {
		t.Fatal("expected identity mismatch")
	}
}
