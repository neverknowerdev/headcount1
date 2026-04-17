package db

import (
	"time"
)

type Company struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	ShortName string    `json:"short_name" gorm:"not null"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Project struct {
	ID              int32     `json:"id" gorm:"primaryKey"`
	CompanyID       int32     `json:"company_id" gorm:"not null"`
	Company         Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name            string    `json:"name" gorm:"not null"`
	Description     string    `json:"description"`
	WorkspaceFolder string    `json:"workspace_folder"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Sprint struct {
	ID          int32      `json:"id" gorm:"primaryKey"`
	CompanyID   int32      `json:"company_id" gorm:"not null"`
	Company     Company    `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name        string     `json:"name" gorm:"not null"`
	Goal        string     `json:"goal"`
	StartDate   *time.Time `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type LLMProvider struct {
	ID              int32     `json:"id" gorm:"primaryKey"`
	Name            string    `json:"name" gorm:"not null"`
	BaseUrl         string    `json:"base_url" gorm:"not null"`
	ApiKey          string    `json:"api_key" gorm:"not null"`
	DefaultModel    string    `json:"default_model"`
	SupportedModels string    `json:"supported_models"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Agent struct {
	ID           int32        `json:"id" gorm:"primaryKey"`
	CompanyID    int32        `json:"company_id" gorm:"not null"`
	Company      Company      `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name         string       `json:"name" gorm:"not null"`
	Description  string       `json:"description"`
	SystemPrompt string       `json:"system_prompt" gorm:"not null"`
	ProviderID   *int32       `json:"provider_id"`
	Provider     *LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:SET NULL;"`
	Model        string       `json:"model"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	Skills       []Skill      `json:"skills" gorm:"many2many:agent_skills;"`
}

type Skill struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	CompanyID   int32     `json:"company_id" gorm:"not null"`
	Company     Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description"`
	SourceUrl   string    `json:"source_url"`
	LocalPath   string    `json:"local_path" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Task struct {
	ID          int32      `json:"id" gorm:"primaryKey"`
	CompanyID   int32      `json:"company_id" gorm:"not null"`
	Company     Company    `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID   *int32     `json:"project_id"`
	Project     *Project   `json:"project" gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL;"`
	SprintID    int32      `json:"sprint_id" gorm:"not null"`
	Sprint      Sprint     `json:"sprint" gorm:"foreignKey:SprintID;constraint:OnDelete:CASCADE;"`
	AgentID     *int32     `json:"agent_id"`
	Agent       *Agent     `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:SET NULL;"`
	ParentID    *int32     `json:"parent_id"`
	Parent      *Task      `json:"parent" gorm:"foreignKey:ParentID;constraint:OnDelete:SET NULL;"`
	Title       string     `json:"title" gorm:"not null"`
	Description string     `json:"description"`
	Priority    string     `json:"priority" gorm:"not null;default:'Normal'"`
	Status      string     `json:"status" gorm:"not null;default:'backlog'"`
	DueDate     *time.Time `json:"due_date"`
	IsArchived  bool       `json:"is_archived" gorm:"not null;default:false"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Comment struct {
	ID         int32     `json:"id" gorm:"primaryKey"`
	TaskID     int32     `json:"task_id" gorm:"not null"`
	Task       Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AuthorType string    `json:"author_type" gorm:"not null"`
	AuthorID   *int32    `json:"author_id"`
	Content    string    `json:"content" gorm:"not null"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Attachment struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	TaskID    int32     `json:"task_id" gorm:"not null"`
	Task      Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	CommentID *int32    `json:"comment_id"`
	Comment   *Comment  `json:"comment" gorm:"foreignKey:CommentID;constraint:OnDelete:CASCADE;"`
	Filename  string    `json:"filename" gorm:"not null"`
	FilePath  string    `json:"file_path" gorm:"not null"`
	MimeType  string    `json:"mime_type"`
	CreatedAt time.Time `json:"created_at"`
}

type Run struct {
	ID          int32      `json:"id" gorm:"primaryKey"`
	TaskID      int32      `json:"task_id" gorm:"not null"`
	Task        Task       `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AgentID     int32      `json:"agent_id" gorm:"not null"`
	Agent       Agent      `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	Status      string     `json:"status" gorm:"not null"`
	LogFilePath string     `json:"log_file_path"`
	LogContent  string     `json:"log_content"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at"`
}

type ActivityLog struct {
	ID         int32     `json:"id" gorm:"primaryKey"`
	CompanyID  int32     `json:"company_id" gorm:"not null"`
	Company    Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Action     string    `json:"action" gorm:"not null"`      // e.g., "task_created", "task_status_updated", "agent_run_started"
	EntityID   int32     `json:"entity_id"`                   // Optional, ID of the task, agent, etc.
	EntityType string    `json:"entity_type"`                 // e.g., "task", "agent", "skill"
	Details    string    `json:"details"`                     // JSON string with more context
	CreatedAt  time.Time `json:"created_at"`
}

type ProxyRequestLog struct {
	ID               int32       `json:"id" gorm:"primaryKey"`
	AgentID          int32       `json:"agent_id" gorm:"not null"`
	Agent            Agent       `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	ProviderID       int32       `json:"provider_id" gorm:"not null"`
	Provider         LLMProvider `json:"provider" gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE;"`
	Model            string      `json:"model" gorm:"not null"`
	PromptTokens     int         `json:"prompt_tokens"`
	CompletionTokens int         `json:"completion_tokens"`
	TotalTokens      int         `json:"total_tokens"`
	CreatedAt        time.Time   `json:"created_at"`
}
