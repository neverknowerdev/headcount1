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

	err = manager.CreateCompanyDirectories(company)
	assert.NoError(t, err)

	expectedDirs := []string{
		"companies/test-co",
		"companies/test-co/memory",
		"companies/test-co/skills",
		"companies/test-co/docs",
		"companies/test-co/agents",
		"companies/test-co/projects",
	}

	for _, d := range expectedDirs {
		p := filepath.Join(tempDir, d)
		info, err := os.Stat(p)
		assert.NoError(t, err)
		assert.True(t, info.IsDir())
	}

	settingsPath := filepath.Join(tempDir, "companies/test-co/settings.yml")
	info, err := os.Stat(settingsPath)
	assert.NoError(t, err)
	assert.False(t, info.IsDir())

	project := db.Project{ID: 1, Name: "project-1"}
	err = manager.CreateProjectDirectories(company, project)
	assert.NoError(t, err)

	expectedProjDirs := []string{
		"companies/test-co/projects/project-1",
		"companies/test-co/projects/project-1/memory",
		"companies/test-co/projects/project-1/docs",
		"companies/test-co/projects/project-1/workspace",
		"companies/test-co/projects/project-1/repo/docs",
	}

	for _, d := range expectedProjDirs {
		p := filepath.Join(tempDir, d)
		info, err := os.Stat(p)
		assert.NoError(t, err)
		assert.True(t, info.IsDir())
	}

	task := db.Task{ID: 44}
	err = manager.CreateTaskWorkspace(company, project, task)
	assert.NoError(t, err)

	taskPath := filepath.Join(tempDir, "companies/test-co/projects/project-1/workspace/task-44")
	info, err = os.Stat(taskPath)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())
}
