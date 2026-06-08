package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
)

// initTaskMemory creates the initial memory.md file for a task if it doesn't
// already exist. The file provides context to the agent about the task and
// company. The logic is expected to grow as we add more context (e.g. sprint
// goals, prior run summaries, etc.).
func initTaskMemory(workspacePath string, task db.Task, company db.Company) error {
	memoryPath := filepath.Join(workspacePath, "memory.md")
	if _, statErr := os.Stat(memoryPath); !os.IsNotExist(statErr) {
		return nil // already exists
	}

	initialMemory := fmt.Sprintf(
		"# Task %d: %s\nCompany: %s\n\n%s\n",
		task.ID, task.Title, company.Name, task.Description,
	)
	return os.WriteFile(memoryPath, []byte(initialMemory), 0644)
}
