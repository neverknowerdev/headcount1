package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/mempalace"
)

// initTaskMemory creates the initial memory.md file for a task if it doesn't
// already exist. The file provides context to the agent about the task and
// company. When the MemPalace memory layer is available, a wake-up snapshot
// of the project's wing (identity + essential story) is appended so the
// workspace file carries institutional context instead of just the task text.
func initTaskMemory(workspacePath string, task db.Task, company db.Company) error {
	memoryPath := filepath.Join(workspacePath, "memory.md")
	if _, statErr := os.Stat(memoryPath); !os.IsNotExist(statErr) {
		return nil // already exists
	}

	initialMemory := fmt.Sprintf(
		"# Task %d: %s\nCompany: %s\n\n%s\n",
		task.ID, task.Title, company.Name, task.Description,
	)

	if mempalace.Available() {
		wing := mempalace.CompanyWing
		if task.Project != nil && task.Project.Name != "" {
			wing = mempalace.ProjectWing(task.Project.Name)
		}
		if wake := mempalace.WakeUp(company, wing); wake != "" {
			initialMemory += fmt.Sprintf("\n## Memory wake-up (%s)\n%s\n", wing, wake)
		}
	}
	return os.WriteFile(memoryPath, []byte(initialMemory), 0644)
}
