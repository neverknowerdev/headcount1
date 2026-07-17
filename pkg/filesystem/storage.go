package filesystem

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"
)

// CompanyMeta is the filesystem representation of a company
type CompanyMeta struct {
	ID        int32  `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	Color     string `json:"color"`
	// Tenancy — see CompanySettings; nil in files from before teams.
	TeamID *int32 `json:"team_id,omitempty"`
	UserID *int32 `json:"user_id,omitempty"`
}

// ProjectMeta is the filesystem representation of a project
type ProjectMeta struct {
	ID              int32  `json:"id"`
	CompanyID       int32  `json:"company_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	WorkspaceFolder string `json:"workspace_folder"`
	RepositoryUrl   string `json:"repository_url,omitempty"`
}

// SprintMeta is the filesystem representation of a sprint
type SprintMeta struct {
	ID        int32   `json:"id"`
	CompanyID int32   `json:"company_id"`
	Name      string  `json:"name"`
	Goal      string  `json:"goal"`
	StartDate *string `json:"start_date,omitempty"`
	EndDate   *string `json:"end_date,omitempty"`
}

// TaskMeta is the filesystem representation of a task
type TaskMeta struct {
	ID          int32   `json:"id"`
	CompanyID   int32   `json:"company_id"`
	ProjectID   *int32  `json:"project_id,omitempty"`
	SprintID    int32   `json:"sprint_id"`
	AgentID     *int32  `json:"agent_id,omitempty"`
	ParentID    *int32  `json:"parent_id,omitempty"`
	Title       string  `json:"title"`
	TaskType    string  `json:"task_type"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	DueDate     *string `json:"due_date,omitempty"`
	IsArchived  bool    `json:"is_archived"`
}

// AgentMeta is the filesystem representation of an agent
type AgentMeta struct {
	ID           int32  `json:"id"`
	CompanyID    int32  `json:"company_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model"`
	Mode         string `json:"mode"`
	Permissions  string `json:"permissions"`
	ProviderID   *int32 `json:"provider_id,omitempty"`
}

// ProviderMeta is the filesystem representation of an LLM provider. New
// files carry the API key only encrypted (api_key_enc); the plaintext
// api_key field remains so files written before encryption still restore.
// ReadProviders resolves whichever is present into ApiKey (plaintext, in
// memory only).
type ProviderMeta struct {
	ID              int32  `json:"id"`
	Name            string `json:"name"`
	BaseUrl         string `json:"base_url"`
	ApiKey          string `json:"api_key,omitempty"`
	ApiKeyEnc       string `json:"api_key_enc,omitempty"`
	ProviderType    string `json:"provider_type"`
	DefaultModel    string `json:"default_model"`
	SupportedModels string `json:"supported_models"`
}

// CommentMeta is the filesystem representation of a comment
type CommentMeta struct {
	ID         int32  `json:"id"`
	TaskID     int32  `json:"task_id"`
	AuthorType string `json:"author_type"`
	AuthorID   *int32 `json:"author_id,omitempty"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// AttachmentMeta is the filesystem representation of an attachment
type AttachmentMeta struct {
	ID       int32  `json:"id"`
	TaskID   int32  `json:"task_id"`
	Filename string `json:"filename"`
	FilePath string `json:"file_path"`
	MimeType string `json:"mime_type,omitempty"`
}

// RunMeta is the filesystem representation of a run
type RunMeta struct {
	ID              int32  `json:"id"`
	TaskID          int32  `json:"task_id"`
	AgentID         int32  `json:"agent_id"`
	ParentRunID     *int32 `json:"parent_run_id,omitempty"`
	RootRunID       *int32 `json:"root_run_id,omitempty"`
	AgentConfigName string `json:"agent_config_name,omitempty"`
	Status          string `json:"status"`
	StartedAt       string `json:"started_at,omitempty"`
	EndedAt         string `json:"ended_at,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	LogContent      string `json:"log_content,omitempty"`
}

// ActivityLogMeta is the filesystem representation of an activity log
type ActivityLogMeta struct {
	ID         int32  `json:"id"`
	CompanyID  int32  `json:"company_id"`
	Action     string `json:"action"`
	EntityID   int32  `json:"entity_id"`
	EntityType string `json:"entity_type"`
	Details    string `json:"details,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

type Storage struct {
	basePath string
}

func NewStorage(basePath string) *Storage {
	return &Storage{basePath: basePath}
}

// --- Companies ---

func (s *Storage) WriteCompany(comp db.Company) error {
	dir := filepath.Join(s.basePath, "data", comp.ShortName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := CompanyMeta{
		ID:        comp.ID,
		Name:      comp.Name,
		ShortName: comp.ShortName,
		Color:     comp.Color,
		TeamID:    comp.TeamID,
		UserID:    comp.UserID,
	}
	return writeJSON(filepath.Join(dir, "company.json"), meta)
}

func (s *Storage) ReadCompany(shortName string) (*CompanyMeta, error) {
	var meta CompanyMeta
	if err := readJSON(filepath.Join(s.basePath, "data", shortName, "company.json"), &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (s *Storage) ListCompanyDirs() ([]string, error) {
	dataPath := filepath.Join(s.basePath, "data")
	entries, err := os.ReadDir(dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "memory" && entry.Name() != "artifacts" && entry.Name() != "skills" && entry.Name() != "logs" && entry.Name() != "llm-providers" && entry.Name() != "activity-logs" && entry.Name() != "mcp-servers" && entry.Name() != "runs" && entry.Name() != "docker" {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs, nil
}

// --- Projects ---

func (s *Storage) WriteProject(comp db.Company, proj db.Project) error {
	dir := filepath.Join(s.basePath, "data", "artifacts", comp.ShortName, proj.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := ProjectMeta{
		ID:              proj.ID,
		CompanyID:       proj.CompanyID,
		Name:            proj.Name,
		Description:     proj.Description,
		WorkspaceFolder: proj.WorkspaceFolder,
		RepositoryUrl:   proj.RepositoryUrl,
	}
	return writeJSON(filepath.Join(dir, "project.json"), meta)
}

func (s *Storage) ListProjectDirs(companyShortName string) ([]string, error) {
	projPath := filepath.Join(s.basePath, "data", "artifacts", companyShortName)
	entries, err := os.ReadDir(projPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs, nil
}

func (s *Storage) ReadProject(companyShortName, projectName string) (*ProjectMeta, error) {
	var meta ProjectMeta
	if err := readJSON(filepath.Join(s.basePath, "data", "artifacts", companyShortName, projectName, "project.json"), &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// --- Sprints ---

func (s *Storage) WriteSprint(sprint db.Sprint, companyShortName string) error {
	dir := filepath.Join(s.basePath, "data", companyShortName, "sprints")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := SprintMeta{
		ID:        sprint.ID,
		CompanyID: sprint.CompanyID,
		Name:      sprint.Name,
		Goal:      sprint.Goal,
	}
	if sprint.StartDate != nil {
		s := sprint.StartDate.Format("2006-01-02T15:04:05Z")
		meta.StartDate = &s
	}
	if sprint.EndDate != nil {
		s := sprint.EndDate.Format("2006-01-02T15:04:05Z")
		meta.EndDate = &s
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", sprint.ID)), meta)
}

func (s *Storage) ReadSprints(companyShortName string) ([]SprintMeta, error) {
	dir := filepath.Join(s.basePath, "data", companyShortName, "sprints")
	return readJSONDir[SprintMeta](dir)
}

// --- Tasks ---

func (s *Storage) WriteTask(task db.Task, companyShortName string) error {
	dir := filepath.Join(s.basePath, "workspace", companyShortName, fmt.Sprintf("task-%d", task.ID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := TaskMeta{
		ID:          task.ID,
		CompanyID:   task.CompanyID,
		ProjectID:   task.ProjectID,
		SprintID:    task.SprintID,
		AgentID:     task.AgentID,
		ParentID:    task.ParentID,
		Title:       task.Title,
		TaskType:    task.TaskType,
		Description: task.Description,
		Status:      task.Status,
		Priority:    task.Priority,
		IsArchived:  task.IsArchived,
	}
	if task.DueDate != nil {
		d := task.DueDate.Format("2006-01-02T15:04:05Z")
		meta.DueDate = &d
	}
	return writeJSON(filepath.Join(dir, "task.json"), meta)
}

func (s *Storage) ReadTasks(companyShortName string) ([]TaskMeta, error) {
	workspaceDir := filepath.Join(s.basePath, "workspace", companyShortName)
	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []TaskMeta{}, nil
		}
		return nil, err
	}
	var tasks []TaskMeta
	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) > 5 && entry.Name()[:5] == "task-" {
			var meta TaskMeta
			if err := readJSON(filepath.Join(workspaceDir, entry.Name(), "task.json"), &meta); err == nil {
				tasks = append(tasks, meta)
			}
		}
	}
	return tasks, nil
}

// --- Agents ---

func (s *Storage) WriteAgent(agent db.Agent, companyShortName string) error {
	dir := filepath.Join(s.basePath, "data", companyShortName, "agents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := AgentMeta{
		ID:           agent.ID,
		CompanyID:    agent.CompanyID,
		Name:         agent.Name,
		Description:  agent.Description,
		SystemPrompt: agent.SystemPrompt,
		Model:        agent.Model,
		Mode:         agent.Mode,
		Permissions:  agent.Permissions,
		ProviderID:   agent.ProviderID,
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", agent.ID)), meta)
}

func (s *Storage) ReadAgents(companyShortName string) ([]AgentMeta, error) {
	dir := filepath.Join(s.basePath, "data", companyShortName, "agents")
	return readJSONDir[AgentMeta](dir)
}

// --- LLM Providers ---

func (s *Storage) WriteProvider(provider db.LLMProvider) error {
	dir := filepath.Join(s.basePath, "data", "llm-providers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	sealed, err := secrets.Default().Seal(provider.ApiKey)
	if err != nil {
		return fmt.Errorf("encrypt provider %d api key for filesystem mirror: %w", provider.ID, err)
	}
	meta := ProviderMeta{
		ID:              provider.ID,
		Name:            provider.Name,
		BaseUrl:         provider.BaseUrl,
		ApiKeyEnc:       sealed,
		ProviderType:    provider.ProviderType,
		DefaultModel:    provider.DefaultModel,
		SupportedModels: provider.SupportedModels,
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", provider.ID)), meta)
}

func (s *Storage) ReadProviders() ([]ProviderMeta, error) {
	dir := filepath.Join(s.basePath, "data", "llm-providers")
	metas, err := readJSONDir[ProviderMeta](dir)
	for i := range metas {
		stored := metas[i].ApiKeyEnc
		if stored == "" {
			stored = metas[i].ApiKey // pre-encryption file
		}
		key, openErr := secrets.Default().Open(stored)
		if openErr != nil {
			log.Printf("Skipping api key of provider %d (cannot decrypt): %v", metas[i].ID, openErr)
			key = ""
		}
		metas[i].ApiKey = key
		metas[i].ApiKeyEnc = ""
	}
	return metas, err
}

// --- Comments ---

func (s *Storage) WriteComment(comment db.Comment, companyShortName string) error {
	dir := filepath.Join(s.basePath, "workspace", companyShortName, fmt.Sprintf("task-%d", comment.TaskID), "comments")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := CommentMeta{
		ID:         comment.ID,
		TaskID:     comment.TaskID,
		AuthorType: comment.AuthorType,
		AuthorID:   comment.AuthorID,
		Content:    comment.Content,
		CreatedAt:  comment.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", comment.ID)), meta)
}

func (s *Storage) ReadComments(companyShortName string, taskID int32) ([]CommentMeta, error) {
	dir := filepath.Join(s.basePath, "workspace", companyShortName, fmt.Sprintf("task-%d", taskID), "comments")
	return readJSONDir[CommentMeta](dir)
}

// --- Attachments ---

func (s *Storage) WriteAttachment(attachment db.Attachment, companyShortName string) error {
	dir := filepath.Join(s.basePath, "workspace", companyShortName, fmt.Sprintf("task-%d", attachment.TaskID), "attachments")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := AttachmentMeta{
		ID:       attachment.ID,
		TaskID:   attachment.TaskID,
		Filename: attachment.Filename,
		FilePath: attachment.FilePath,
		MimeType: attachment.MimeType,
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", attachment.ID)), meta)
}

func (s *Storage) ReadAttachments(companyShortName string, taskID int32) ([]AttachmentMeta, error) {
	dir := filepath.Join(s.basePath, "workspace", companyShortName, fmt.Sprintf("task-%d", taskID), "attachments")
	return readJSONDir[AttachmentMeta](dir)
}

// --- Runs ---

func (s *Storage) WriteRun(run db.Run, companyShortName string) error {
	dir := filepath.Join(s.basePath, "data", "runs", companyShortName, fmt.Sprintf("task-%d", run.TaskID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := RunMeta{
		ID:              run.ID,
		TaskID:          run.TaskID,
		AgentID:         run.AgentID,
		ParentRunID:     run.ParentRunID,
		RootRunID:       run.RootRunID,
		AgentConfigName: run.AgentConfigName,
		Status:          run.Status,
		SessionID:       run.SessionID,
		LogContent:      run.LogContent,
		StartedAt:       run.StartedAt.Format("2006-01-02T15:04:05Z"),
	}
	if run.EndedAt != nil {
		meta.EndedAt = run.EndedAt.Format("2006-01-02T15:04:05Z")
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", run.ID)), meta)
}

func (s *Storage) ReadRuns(companyShortName string, taskID int32) ([]RunMeta, error) {
	dir := filepath.Join(s.basePath, "data", "runs", companyShortName, fmt.Sprintf("task-%d", taskID))
	return readJSONDir[RunMeta](dir)
}

// --- Activity Logs ---

func (s *Storage) WriteActivityLog(log db.ActivityLog) error {
	dir := filepath.Join(s.basePath, "data", "activity-logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	meta := ActivityLogMeta{
		ID:         log.ID,
		CompanyID:  log.CompanyID,
		Action:     log.Action,
		EntityID:   log.EntityID,
		EntityType: log.EntityType,
		Details:    log.Details,
		CreatedAt:  log.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	return writeJSON(filepath.Join(dir, fmt.Sprintf("%d.json", log.ID)), meta)
}

func (s *Storage) ReadActivityLogs() ([]ActivityLogMeta, error) {
	dir := filepath.Join(s.basePath, "data", "activity-logs")
	return readJSONDir[ActivityLogMeta](dir)
}

// --- Helpers ---

func writeJSON(path string, data interface{}) error {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, bytes, 0644)
}

func readJSON(path string, out interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func readJSONDir[T any](dir string) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []T{}, nil
		}
		return nil, err
	}
	var items []T
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			var item T
			if err := readJSON(filepath.Join(dir, entry.Name()), &item); err == nil {
				items = append(items, item)
			}
		}
	}
	return items, nil
}

// ParseInt32 safely converts a string to int32
func ParseInt32(s string) int32 {
	n, _ := strconv.Atoi(s)
	return int32(n)
}

// GetCompanyShortNameForTask finds the company short name for a task by looking at workspace dirs
func (s *Storage) GetCompanyShortNameForTask(taskID int32) (string, error) {
	workspaceDir := filepath.Join(s.basePath, "workspace")
	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return "", err
	}
	taskDirName := fmt.Sprintf("task-%d", taskID)
	for _, companyEntry := range entries {
		if companyEntry.IsDir() {
			taskPath := filepath.Join(workspaceDir, companyEntry.Name(), taskDirName)
			if _, err := os.Stat(taskPath); err == nil {
				return companyEntry.Name(), nil
			}
		}
	}
	return "", fmt.Errorf("task %d not found in workspace", taskID)
}

// GetCompanyShortNameForActivityLog finds the company short name from an activity log's company_id
func (s *Storage) GetCompanyShortNameForActivityLog(companyID int32) (string, error) {
	companies, err := s.ListCompanyDirs()
	if err != nil {
		return "", err
	}
	for _, shortName := range companies {
		meta, err := s.ReadCompany(shortName)
		if err == nil && meta.ID == companyID {
			return shortName, nil
		}
	}
	return "", fmt.Errorf("company %d not found", companyID)
}
