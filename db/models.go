package db

import (
	"time"
)

type Company struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Project struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	CompanyID   int32     `json:"company_id" gorm:"not null"`
	Company     Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type LLMProvider struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	Name      string    `json:"name" gorm:"not null"`
	BaseUrl   string    `json:"base_url" gorm:"not null"`
	ApiKey    string    `json:"api_key" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	ID          int32     `json:"id" gorm:"primaryKey"`
	ProjectID   int32     `json:"project_id" gorm:"not null"`
	Project     Project   `json:"project" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	AgentID     *int32    `json:"agent_id"`
	Agent       *Agent    `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:SET NULL;"`
	Title       string    `json:"title" gorm:"not null"`
	Description string    `json:"description"`
	Status      string    `json:"status" gorm:"not null;default:'backlog'"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
