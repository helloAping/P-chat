package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

var (
	// ErrRequired indicates the project name or path was empty.
	ErrRequired = errors.New("project name and path are required")
	// ErrPathNotAbsolute indicates the project path is not absolute.
	ErrPathNotAbsolute = errors.New("project path must be absolute")
	// ErrPathNotDirectory indicates the project path does not exist as a directory.
	ErrPathNotDirectory = errors.New("project path must be an existing directory")
	// ErrDuplicatePath indicates the project path is already registered.
	ErrDuplicatePath = errors.New("project path already exists")
)

// Project represents a user-registered project directory.
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Load reads the project list from ~/.p-chat/projects.json.
// Returns an empty slice when the file is missing.
func Load() ([]Project, error) {
	data, err := os.ReadFile(paths.ProjectsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects: %w", err)
	}
	var projects []Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, fmt.Errorf("parse projects: %w", err)
	}
	return projects, nil
}

// Save writes the project list to ~/.p-chat/projects.json.
func Save(projects []Project) error {
	if err := os.MkdirAll(paths.ProjectsFileDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(projects, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal projects: %w", err)
	}
	return os.WriteFile(paths.ProjectsFile(), data, 0o644)
}

// Add appends a project to the list, deduplicating by path.
func Add(name, path string) ([]Project, error) {
	name = strings.TrimSpace(name)
	path = filepath.Clean(strings.TrimSpace(path))
	if name == "" || path == "" || path == "." {
		return nil, ErrRequired
	}
	if !filepath.IsAbs(path) {
		return nil, ErrPathNotAbsolute
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, ErrPathNotDirectory
	}
	projects, err := Load()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if sameProjectPath(p.Path, path) {
			return nil, ErrDuplicatePath
		}
	}
	projects = append(projects, Project{Name: name, Path: path})
	if err := Save(projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func sameProjectPath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Remove deletes a project from the list by path.
func Remove(path string) ([]Project, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	projects, err := Load()
	if err != nil {
		return nil, err
	}
	filtered := make([]Project, 0, len(projects))
	for _, p := range projects {
		if !sameProjectPath(p.Path, path) {
			filtered = append(filtered, p)
		}
	}
	if err := Save(filtered); err != nil {
		return nil, err
	}
	return filtered, nil
}
