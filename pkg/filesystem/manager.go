package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
)

type Manager struct {
	basePath string
}

func NewManager(basePath string) *Manager {
	if basePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			basePath = "/tmp/.paperclip2"
		} else {
			basePath = filepath.Join(homeDir, ".paperclip2")
		}
	}
	return &Manager{basePath: basePath}
}

func (m *Manager) GetBasePath() string {
	return m.basePath
}

func (m *Manager) CreateCompanyDirectories(company db.Company) error {
	return nil
}

func (m *Manager) CreateProjectDirectories(company db.Company, project db.Project) error {
	return nil
}

func (m *Manager) PrepareProjectRepo(ctx context.Context, company db.Company, project db.Project) error {
	if project.RepositoryUrl == "" {
		return nil
	}

	repoDir := filepath.Join(m.basePath, "data", "artifacts", company.ShortName, project.Name)
	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return err
	}
	return nil
}

func (m *Manager) CreateTaskWorkspace(company db.Company, project db.Project, task db.Task) error {
	taskPath := filepath.Join(m.basePath, "workspace", company.ShortName, fmt.Sprintf("task-%d", task.ID))
	if err := os.MkdirAll(taskPath, 0755); err != nil {
		return fmt.Errorf("failed to create task workspace: %w", err)
	}

	// Init memory.md
	memoryPath := filepath.Join(taskPath, "memory.md")
	if _, err := os.Stat(memoryPath); os.IsNotExist(err) {
		initialMemory := fmt.Sprintf("# Task %d: %s\nCompany: %s\n\n%s\n", task.ID, task.Title, company.Name, task.Description)
		os.WriteFile(memoryPath, []byte(initialMemory), 0644)
	}

	return nil
}

func (m *Manager) CreateSkillDirectory(company db.Company, skill db.Skill) error {
	skillPath := filepath.Join(m.basePath, "data", "skills", company.ShortName, skill.Name)
	if err := os.MkdirAll(skillPath, 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}
	return nil
}

func (m *Manager) GetSkillPath(company db.Company, skill db.Skill) string {
	return filepath.Join(m.basePath, "data", "skills", company.ShortName, skill.Name)
}

func (m *Manager) GetCompanyPath(company db.Company) string {
	return filepath.Join(m.basePath, "data", company.ShortName)
}

func (m *Manager) SetupBaseDirectories() error {
	dirs := []string{
		filepath.Join(m.basePath, "workspace"),
		filepath.Join(m.basePath, "data"),
		filepath.Join(m.basePath, "data", "memory"),
		filepath.Join(m.basePath, "data", "artifacts"),
		filepath.Join(m.basePath, "data", "skills"),
		filepath.Join(m.basePath, "data", "skills", "basic"),
		filepath.Join(m.basePath, "data", "logs"),
		filepath.Join(m.basePath, "data", "docker"),
		filepath.Join(m.basePath, ".ssh"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	gitignorePath := filepath.Join(m.basePath, "data", ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		os.WriteFile(gitignorePath, []byte("artifacts/\n"), 0644)
	}

	return nil
}
