package project

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/p-chat/pchat/internal/paths"
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
	projects, err := Load()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if p.Path == path {
			return projects, nil
		}
	}
	projects = append(projects, Project{Name: name, Path: path})
	if err := Save(projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// Remove deletes a project from the list by path.
func Remove(path string) ([]Project, error) {
	projects, err := Load()
	if err != nil {
		return nil, err
	}
	filtered := make([]Project, 0, len(projects))
	for _, p := range projects {
		if p.Path != path {
			filtered = append(filtered, p)
		}
	}
	if err := Save(filtered); err != nil {
		return nil, err
	}
	return filtered, nil
}
