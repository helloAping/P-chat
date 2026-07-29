package server

// projects.go — registered project directory CRUD.
//
//   GET    /api/v1/projects
//   POST   /api/v1/projects
//   DELETE /api/v1/projects/:path
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/project"
)

func (h *Handler) ListProjects(c *gin.Context) {
	projects, err := project.Load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if projects == nil {
		projects = []project.Project{}
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectResponse{Name: p.Name, Path: p.Path})
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

// AddProject POST /api/v1/projects
func (h *Handler) AddProject(c *gin.Context) {
	var req projectResponse
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Path = strings.TrimSpace(req.Path)
	if req.Name == "" || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and path are required"})
		return
	}
	projects, err := project.Add(req.Name, req.Path)
	if err != nil {
		writeProjectError(c, err)
		return
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectResponse{Name: p.Name, Path: p.Path})
	}
	c.JSON(http.StatusCreated, gin.H{"projects": out})
}

// RemoveProject DELETE /api/v1/projects
func (h *Handler) RemoveProject(c *gin.Context) {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	// Archive all sessions associated with this project.
	if h.store != nil {
		h.store.ArchiveByProjectPath(req.Path)
	}
	projects, err := project.Remove(req.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]projectResponse, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectResponse{Name: p.Name, Path: p.Path})
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

func writeProjectError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, project.ErrRequired):
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and path are required"})
	case errors.Is(err, project.ErrPathNotAbsolute):
		c.JSON(http.StatusBadRequest, gin.H{"error": "project path must be absolute"})
	case errors.Is(err, project.ErrPathNotDirectory):
		c.JSON(http.StatusBadRequest, gin.H{"error": "project path must be an existing directory"})
	case errors.Is(err, project.ErrDuplicatePath):
		c.JSON(http.StatusConflict, gin.H{"error": "project path already exists"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// contextMessageLimit returns the message fetch limit for the given
// provider/model pair, scaled to the model's configured context window.
// When LimitsConfig.MaxStoredMessages is set (> 0) it takes precedence.
// Otherwise the limit is max(50, contextWindow / 2000), capped at 1000.
