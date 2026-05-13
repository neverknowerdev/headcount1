package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"

	"github.com/stretchr/testify/assert"
)

func TestFilesystemManager(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fs-manager-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager := NewManager(tempDir)
	company := db.Company{ID: 1, Name: "Test Company", ShortName: "test-co"}

	err = manager.SetupBaseDirectories()
	assert.NoError(t, err)

	expectedDirs := []string{
		"workspace",
		"data",
		"data/memory",
		"data/artifacts",
		"data/skills",
		"data/skills/basic",
		"data/logs",
		"data/docker",
		".ssh",
	}

	for _, d := range expectedDirs {
		p := filepath.Join(tempDir, d)
		info, err := os.Stat(p)
		assert.NoError(t, err)
		assert.True(t, info.IsDir())
	}

	project := db.Project{ID: 1, Name: "project-1", WorkspaceFolder: "workspace/test-co/project-1"}
	task := db.Task{ID: 44, Title: "Test Task"}

	err = manager.CreateTaskWorkspace(company, project, task)
	assert.NoError(t, err)

	taskPath := filepath.Join(tempDir, "workspace/test-co/task-44")
	info, err := os.Stat(taskPath)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())

	memoryPath := filepath.Join(taskPath, "memory.md")
	info, err = os.Stat(memoryPath)
	assert.NoError(t, err)
	assert.False(t, info.IsDir())
}
