package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"

	"github.com/stretchr/testify/assert"
)

func TestFilesystemManager(t *testing.T) {
	tempDir := t.TempDir()

	manager := NewManager(tempDir)
	company := db.Company{ID: 1, Name: "Test Company", ShortName: "test-co"}

	err := manager.SetupBaseDirectories()
	assert.NoError(t, err)

	expectedDirs := []string{
		"db",
		"ssh",
		"credentials",
		"uploads",
		"backups",
		"repos",
		"workspace",
		"artifacts",
		"logs",
		"skills",
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

	assert.Equal(t, filepath.Join(tempDir, "repos", "test-co", "project-1"), manager.GetProjectRepoPath(company, project))
	assert.Equal(t, filepath.Join(tempDir, "skills", "test-co", "review"), manager.GetSkillPath(company, db.Skill{Name: "review"}))
}
