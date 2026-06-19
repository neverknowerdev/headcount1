package filesystem

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/git"
	"gopkg.in/yaml.v3"
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
		if entry.IsDir() && entry.Name() != "memory" && entry.Name() != "artifacts" && entry.Name() != "skills" && entry.Name() != "logs" && entry.Name() != "llm-providers" && entry.Name() != "activity-logs" {
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

	if !hasFiles(companyPath) {
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

func hasFiles(dir string) bool {
	found := false
	metadataFiles := map[string]bool{
		"company.json": true,
		"project.json": true,
	}
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if !metadataFiles[info.Name()] {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// CompanySettings is the content of companies/{shortName}/settings.yml.
type CompanySettings struct {
	CompanyID int32  `yaml:"company_id"`
	Name      string `yaml:"name"`
	ShortName string `yaml:"short_name"`
}

func (m *Manager) WriteCompanySettings(company db.Company) error {
	dir := filepath.Join(m.basePath, "companies", company.ShortName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	s := CompanySettings{
		CompanyID: company.ID,
		Name:      company.Name,
		ShortName: company.ShortName,
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.yml"), data, 0644)
}

func (m *Manager) ReadCompanySettings(shortName string) (*CompanySettings, error) {
	data, err := os.ReadFile(filepath.Join(m.basePath, "companies", shortName, "settings.yml"))
	if err != nil {
		return nil, err
	}
	var s CompanySettings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (m *Manager) SaveLLMProvider(provider db.LLMProvider) error {
	dir := filepath.Join(m.basePath, "data", "llm-providers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(provider, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", provider.ID)), data, 0644)
}

func (m *Manager) ListLLMProvidersFromDisk() ([]db.LLMProvider, error) {
	dir := filepath.Join(m.basePath, "data", "llm-providers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []db.LLMProvider
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("Skipping unreadable provider file %s: %v", e.Name(), err)
			continue
		}
		var p db.LLMProvider
		if err := json.Unmarshal(data, &p); err != nil {
			log.Printf("Skipping invalid provider file %s: %v", e.Name(), err)
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func (m *Manager) DeleteLLMProviderFile(id int32) error {
	path := filepath.Join(m.basePath, "data", "llm-providers", fmt.Sprintf("%d.json", id))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SprintRecord is a flat Sprint for filesystem storage (no nested GORM associations).
type SprintRecord struct {
	ID        int32      `json:"id"`
	CompanyID int32      `json:"company_id"`
	Name      string     `json:"name"`
	Goal      string     `json:"goal"`
	StartDate *time.Time `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (m *Manager) SaveSprint(company db.Company, sprint db.Sprint) error {
	dir := filepath.Join(m.basePath, "companies", company.ShortName, "sprints")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	rec := SprintRecord{
		ID:        sprint.ID,
		CompanyID: sprint.CompanyID,
		Name:      sprint.Name,
		Goal:      sprint.Goal,
		StartDate: sprint.StartDate,
		EndDate:   sprint.EndDate,
		CreatedAt: sprint.CreatedAt,
		UpdatedAt: sprint.UpdatedAt,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", sprint.ID)), data, 0644)
}

func (m *Manager) ListSprintsFromDisk(companyShortName string) ([]SprintRecord, error) {
	dir := filepath.Join(m.basePath, "companies", companyShortName, "sprints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []SprintRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("Skipping unreadable sprint file %s: %v", e.Name(), err)
			continue
		}
		var s SprintRecord
		if err := json.Unmarshal(data, &s); err != nil {
			log.Printf("Skipping invalid sprint file %s: %v", e.Name(), err)
			continue
		}
		result = append(result, s)
	}
	return result, nil
}

// TaskRecord is a flat Task for filesystem storage (no nested GORM associations).
type TaskRecord struct {
	ID          int32      `json:"id"`
	CompanyID   int32      `json:"company_id"`
	ProjectID   *int32     `json:"project_id"`
	SprintID    int32      `json:"sprint_id"`
	AgentID     *int32     `json:"agent_id"`
	ParentID    *int32     `json:"parent_id"`
	Title       string     `json:"title"`
	TaskType    string     `json:"task_type"`
	Description string     `json:"description"`
	Priority    string     `json:"priority"`
	Status      string     `json:"status"`
	DueDate     *time.Time `json:"due_date"`
	IsArchived  bool       `json:"is_archived"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CommentRecord is a flat Comment for filesystem storage (no nested GORM associations).
type CommentRecord struct {
	ID         int32     `json:"id"`
	TaskID     int32     `json:"task_id"`
	AuthorType string    `json:"author_type"`
	AuthorID   *int32    `json:"author_id"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// taskDir returns the per-task directory: companies/{shortName}/tasks/{id}/
func (m *Manager) taskDir(companyShortName string, taskID int32) string {
	return filepath.Join(m.basePath, "companies", companyShortName, "tasks", fmt.Sprintf("%d", taskID))
}

// SaveTask writes task metadata to companies/{shortName}/tasks/{id}/task.json
func (m *Manager) SaveTask(company db.Company, task db.Task) error {
	dir := m.taskDir(company.ShortName, task.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	rec := TaskRecord{
		ID:          task.ID,
		CompanyID:   task.CompanyID,
		ProjectID:   task.ProjectID,
		SprintID:    task.SprintID,
		AgentID:     task.AgentID,
		ParentID:    task.ParentID,
		Title:       task.Title,
		TaskType:    task.TaskType,
		Description: task.Description,
		Priority:    task.Priority,
		Status:      task.Status,
		DueDate:     task.DueDate,
		IsArchived:  task.IsArchived,
		CreatedAt:   task.CreatedAt,
		UpdatedAt:   task.UpdatedAt,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "task.json"), data, 0644)
}

// SaveTaskComments writes all comments for a task to companies/{shortName}/tasks/{id}/comments.json
func (m *Manager) SaveTaskComments(company db.Company, taskID int32, comments []db.Comment) error {
	dir := m.taskDir(company.ShortName, taskID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	recs := make([]CommentRecord, 0, len(comments))
	for _, c := range comments {
		recs = append(recs, CommentRecord{
			ID:         c.ID,
			TaskID:     c.TaskID,
			AuthorType: c.AuthorType,
			AuthorID:   c.AuthorID,
			Content:    c.Content,
			CreatedAt:  c.CreatedAt,
			UpdatedAt:  c.UpdatedAt,
		})
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "comments.json"), data, 0644)
}

// ListTasksFromDisk reads all task directories under companies/{shortName}/tasks/
func (m *Manager) ListTasksFromDisk(companyShortName string) ([]TaskRecord, error) {
	dir := filepath.Join(m.basePath, "companies", companyShortName, "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []TaskRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		taskFile := filepath.Join(dir, e.Name(), "task.json")
		data, err := os.ReadFile(taskFile)
		if err != nil {
			log.Printf("Skipping task dir %s (no task.json): %v", e.Name(), err)
			continue
		}
		var t TaskRecord
		if err := json.Unmarshal(data, &t); err != nil {
			log.Printf("Skipping invalid task.json in %s: %v", e.Name(), err)
			continue
		}
		result = append(result, t)
	}
	return result, nil
}

// ListTaskCommentsFromDisk reads companies/{shortName}/tasks/{id}/comments.json
func (m *Manager) ListTaskCommentsFromDisk(companyShortName string, taskID int32) ([]CommentRecord, error) {
	data, err := os.ReadFile(filepath.Join(m.taskDir(companyShortName, taskID), "comments.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var recs []CommentRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}
