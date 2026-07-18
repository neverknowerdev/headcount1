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
	paths    Paths
}

func NewManager(basePath string) *Manager {
	if basePath == "" {
		basePath = db.Headcount1Home()
	}
	return &Manager{basePath: basePath, paths: NewPaths(basePath)}
}

func (m *Manager) GetBasePath() string {
	return m.basePath
}

// Paths exposes the layout helpers rooted at this manager's base path.
func (m *Manager) Paths() Paths {
	return m.paths
}

func (m *Manager) CreateCompanyDirectories(company db.Company) error {
	for _, dir := range m.paths.CompanyDirs(company.ShortName) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) CreateProjectDirectories(company db.Company, project db.Project) error {
	return os.MkdirAll(m.paths.RepoDir(company.ShortName, project.Name), 0755)
}

func (m *Manager) GetProjectRepoPath(company db.Company, project db.Project) string {
	return m.paths.RepoDir(company.ShortName, project.Name)
}

func (m *Manager) PrepareProjectRepo(ctx context.Context, company db.Company, project db.Project) error {
	if project.RepositoryUrl == "" {
		return nil
	}

	repoDir := m.GetProjectRepoPath(company, project)
	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return err
	}

	gitMgr := git.NewGitManager(repoDir, m.paths.SSHDir())
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
	return m.paths.WorktreeDir(company.ShortName, task.ID)
}

func (m *Manager) CreateSkillDirectory(company db.Company, skill db.Skill) error {
	if err := os.MkdirAll(m.paths.SkillDir(company.ShortName, skill.Name), 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}
	return nil
}

func (m *Manager) GetSkillPath(company db.Company, skill db.Skill) string {
	return m.paths.SkillDir(company.ShortName, skill.Name)
}

func (m *Manager) SetupBaseDirectories() error {
	dirs := []string{
		m.paths.DBDir(),
		m.paths.SSHDir(),
		m.paths.CredentialsDir(),
		m.paths.UploadsDir(),
		m.paths.BackupsDir(),
		m.paths.ReposDir(),
		m.paths.WorkspaceDir(),
		m.paths.ArtifactsDir(),
		m.paths.LogsDir(),
		m.paths.SkillsDir(),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// ArchiveCompany tars every company-scoped directory (repos, workspace,
// artifacts, logs, skills) into archive/{shortName}_{timestamp}.tar.gz.
// Returns "" without error when the company has no files worth archiving.
func (m *Manager) ArchiveCompany(company db.Company) (string, error) {
	roots := []string{}
	for _, dir := range m.paths.CompanyDirs(company.ShortName) {
		if info, err := os.Stat(dir); err == nil && info.IsDir() && hasFiles(dir) {
			roots = append(roots, dir)
		}
	}
	if len(roots) == 0 {
		return "", nil
	}

	archiveDir := m.paths.ArchiveDir()
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

	for _, root := range roots {
		// Entries are prefixed with the top-level dir name (repos/, workspace/,
		// ...) so the archive mirrors the runtime layout under BasePath.
		prefix, err := filepath.Rel(m.basePath, root)
		if err != nil {
			prefix = filepath.Base(root)
		}
		if err := addDirToTar(tarWriter, root, prefix); err != nil {
			return "", fmt.Errorf("failed to create archive: %w", err)
		}
	}

	return archivePath, nil
}

func addDirToTar(tarWriter *tar.Writer, root, prefix string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(prefix, relPath))

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
}

// DeleteCompanyFiles removes every company-scoped directory.
func (m *Manager) DeleteCompanyFiles(company db.Company) error {
	for _, dir := range m.paths.CompanyDirs(company.ShortName) {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func hasFiles(dir string) bool {
	found := false
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
