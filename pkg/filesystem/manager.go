package filesystem

import (
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

func (m *Manager) CreateCompanyDirectories(company db.Company) error {
	companyPath := filepath.Join(m.basePath, "companies", company.ShortName)
	dirs := []string{
		companyPath,
		filepath.Join(companyPath, "memory"),
		filepath.Join(companyPath, "skills"),
		filepath.Join(companyPath, "docs"),
		filepath.Join(companyPath, "agents"),
		filepath.Join(companyPath, "projects"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create initial settings.yml
	settingsPath := filepath.Join(companyPath, "settings.yml")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		initialSettings := []byte(fmt.Sprintf("company_id: %d\nname: %s\nshort_name: %s\n", company.ID, company.Name, company.ShortName))
		if err := os.WriteFile(settingsPath, initialSettings, 0644); err != nil {
			return fmt.Errorf("failed to create settings.yml: %w", err)
		}
	}

	return nil
}

func (m *Manager) CreateProjectDirectories(company db.Company, project db.Project) error {
	projectPath := filepath.Join(m.basePath, "companies", company.ShortName, "projects", project.Name)
	dirs := []string{
		projectPath,
		filepath.Join(projectPath, "memory"),
		filepath.Join(projectPath, "docs"),
		filepath.Join(projectPath, "workspace"),
		filepath.Join(projectPath, "repo", "docs"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

func (m *Manager) CreateTaskWorkspace(company db.Company, project db.Project, task db.Task) error {
	taskPath := filepath.Join(m.basePath, "companies", company.ShortName, "projects", project.Name, "workspace", fmt.Sprintf("task-%d", task.ID))
	if err := os.MkdirAll(taskPath, 0755); err != nil {
		return fmt.Errorf("failed to create task workspace: %w", err)
	}
	return nil
}

func (m *Manager) CreateSkillDirectory(company db.Company, skill db.Skill) error {
	skillPath := filepath.Join(m.basePath, "companies", company.ShortName, "skills", skill.Name)
	if err := os.MkdirAll(skillPath, 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}
	return nil
}

func (m *Manager) GetSkillPath(company db.Company, skill db.Skill) string {
	return filepath.Join(m.basePath, "companies", company.ShortName, "skills", skill.Name)
}

func (m *Manager) GetCompanyPath(company db.Company) string {
	return filepath.Join(m.basePath, "companies", company.ShortName)
}
