package filesystem

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/git"
)

type Manager struct {
	basePath string
}

func NewManager(basePath string) *Manager {
	if basePath == "" {
		basePath = db.PaperclipHome()
	}
	return &Manager{basePath: basePath}
}

func (m *Manager) GetBasePath() string {
	return m.basePath
}

func (m *Manager) CreateCompanyDirectories(company db.Company) error {
	compPath := filepath.Join(m.basePath, "data", company.ShortName)
	return os.MkdirAll(compPath, 0755)
}

func (m *Manager) CompanyExists(company db.Company) bool {
	compPath := filepath.Join(m.basePath, "data", company.ShortName)
	info, err := os.Stat(compPath)
	return err == nil && info.IsDir()
}

func (m *Manager) ListCompanies() ([]string, error) {
	dataPath := filepath.Join(m.basePath, "data")
	entries, err := os.ReadDir(dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	companies := []string{}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "memory" && entry.Name() != "artifacts" && entry.Name() != "skills" && entry.Name() != "logs" && entry.Name() != "docker" {
			companies = append(companies, entry.Name())
		}
	}
	return companies, nil
}

func (m *Manager) CreateProjectDirectories(company db.Company, project db.Project) error {
	projPath := filepath.Join(m.basePath, "data", "artifacts", company.ShortName, project.Name)
	return os.MkdirAll(projPath, 0755)
}

func (m *Manager) ListProjects(companyShortName string) ([]string, error) {
	projectsPath := filepath.Join(m.basePath, "data", "artifacts", companyShortName)
	entries, err := os.ReadDir(projectsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	projects := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			projects = append(projects, entry.Name())
		}
	}
	return projects, nil
}

func (m *Manager) GetProjectRepoPath(company db.Company, project db.Project) string {
	return filepath.Join(m.basePath, "data", "artifacts", company.ShortName, project.Name)
}

func (m *Manager) PrepareProjectRepo(ctx context.Context, company db.Company, project db.Project) error {
	if project.RepositoryUrl == "" {
		return nil
	}

	repoDir := m.GetProjectRepoPath(company, project)
	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return err
	}

	sshDir := filepath.Join(m.basePath, ".ssh")
	gitMgr := git.NewGitManager(repoDir, sshDir)
	return gitMgr.CloneOrFetchProject(ctx, project.RepositoryUrl, repoDir)
}

func (m *Manager) CreateTaskWorkspace(company db.Company, project db.Project, task db.Task) error {
	taskPath := m.GetTaskWorktreePath(company, task)
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

func (m *Manager) GetTaskWorktreePath(company db.Company, task db.Task) string {
	return filepath.Join(m.basePath, "workspace", company.ShortName, fmt.Sprintf("task-%d", task.ID))
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

func (m *Manager) ArchiveCompany(company db.Company) (string, error) {
	companyPath := m.GetCompanyPath(company)
	if _, err := os.Stat(companyPath); os.IsNotExist(err) {
		return "", nil
	}

	archiveDir := filepath.Join(m.basePath, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create archive directory: %w", err)
	}

	timestamp := time.Now().Format("20060102_150405")
	archiveName := fmt.Sprintf("%s_%s.tar.gz", company.ShortName, timestamp)
	archivePath := filepath.Join(archiveDir, archiveName)

	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to create archive file: %w", err)
	}
	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	err = filepath.Walk(companyPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(companyPath, path)
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			if _, err := io.Copy(tarWriter, file); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to create archive: %w", err)
	}

	return archivePath, nil
}

func (m *Manager) DeleteCompanyFiles(company db.Company) error {
	companyPath := m.GetCompanyPath(company)
	if _, err := os.Stat(companyPath); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(companyPath)
}
