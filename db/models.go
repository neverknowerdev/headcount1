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
	RepositoryUrl   string    `json:"repository_url"`
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
	ProviderType    string    `json:"provider_type"`
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
	Mode         string       `json:"mode" gorm:"not null;default:'primary'"`
	Permissions  string       `json:"permissions"`
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

const (
	TaskTypePlanAndImplement = "plan and implement"
	TaskTypeImplement        = "implement"
)

type Task struct {
	ID              int32      `json:"id" gorm:"primaryKey"`
	CompanyID       int32      `json:"company_id" gorm:"not null"`
	Company         Company    `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID       *int32     `json:"project_id"`
	Project         *Project   `json:"project" gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL;"`
	SprintID        int32      `json:"sprint_id" gorm:"not null"`
	Sprint          Sprint     `json:"sprint" gorm:"foreignKey:SprintID;constraint:OnDelete:CASCADE;"`
	AgentID         *int32     `json:"agent_id"`
	Agent           *Agent     `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:SET NULL;"`
	ParentID        *int32     `json:"parent_id"`
	Parent          *Task      `json:"parent" gorm:"foreignKey:ParentID;constraint:OnDelete:SET NULL;"`
	Title           string     `json:"title" gorm:"not null"`
	TaskType        string     `json:"task_type" gorm:"not null;default:'plan and implement'"`
	Description     string     `json:"description"`
	Priority        string     `json:"priority" gorm:"not null;default:'Normal'"`
	Status          string     `json:"status" gorm:"not null;default:'backlog'"`
	DueDate         *time.Time `json:"due_date"`
	IsArchived      bool       `json:"is_archived" gorm:"not null;default:false"`
	RunID           *int32     `json:"run_id"`
	AgentConfigName string     `json:"agent_config_name" gorm:"default:''"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
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
	ID              int32      `json:"id" gorm:"primaryKey"`
	TaskID          int32      `json:"task_id" gorm:"not null"`
	Task            Task       `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AgentID         int32      `json:"agent_id" gorm:"not null"`
	Agent           Agent      `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	Status          string     `json:"status" gorm:"not null"`
	SessionID       string     `json:"session_id"`
	LogFilePath     string     `json:"log_file_path"`
	LogContent      string     `json:"log_content"`
	LogEntries      string     `json:"log_entries" gorm:"type:text"` // JSON array of structured log entries
	TokenStats      string     `json:"token_stats" gorm:"type:text"`  // JSON object with aggregated token counts
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	LastMessageTime *time.Time `json:"last_message_time"`
}

// RunTokenStats holds aggregated token counts for a run. Persisted to
// Run.TokenStats as JSON so the Run Logs UI can render an overall
// breakdown without re-iterating LogEntries on every read.
type RunTokenStats struct {
	PromptTokens     int `json:"prompt_tokens"`     // sum of all LLM request input tokens (provider-reported)
	CompletionTokens int `json:"completion_tokens"` // sum of all LLM response output tokens (provider-reported)
	ReasoningTokens  int `json:"reasoning_tokens"`  // sum of reasoning tokens (provider-reported, or estimated)
	ToolInputTokens  int `json:"tool_input_tokens"` // sum of tool call argument sizes (estimated, chars/4)
	ToolOutputTokens int `json:"tool_output_tokens"` // sum of tool response sizes (estimated, chars/4)
	CachedTokens     int `json:"cached_tokens"`     // sum of cached prompt tokens (subset of PromptTokens)
	TotalTokens      int `json:"total_tokens"`      // sum of everything above (excludes CachedTokens)
}

// MCPServer stores configuration for an MCP (Model Context Protocol) server.
type MCPServer struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	CompanyID   int32     `json:"company_id" gorm:"not null"`
	Company     Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name        string    `json:"name" gorm:"not null"`         // unique slug, e.g. "github"
	DisplayName string    `json:"display_name"`                  // e.g. "GitHub MCP"
	Description string    `json:"description"`                   // brief description sent to agents
	Transport   string    `json:"transport" gorm:"not null"`     // "stdio", "http", "builtin"
	Command     string    `json:"command"`                       // stdio: executable path
	Args        string    `json:"args" gorm:"type:text"`         // stdio: JSON array of string args
	URL         string    `json:"url"`                           // http: base URL
	Headers     string    `json:"headers" gorm:"type:text"`      // http: JSON object of extra headers
	AuthType    string    `json:"auth_type"`                     // "none", "bearer", "oauth2"
	AuthToken   string    `json:"auth_token"`                    // bearer token (stored plaintext for now)
	Enabled     bool      `json:"enabled" gorm:"not null;default:true"`
	Builtin     bool      `json:"builtin" gorm:"not null;default:false"` // pre-defined, cannot be deleted
	Agents      []Agent   `json:"agents,omitempty" gorm:"many2many:agent_mcp_servers;"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentMCPServer is the join table for the Agent <-> MCPServer many-to-many.
type AgentMCPServer struct {
	AgentID     int32 `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32 `json:"mcp_server_id" gorm:"primaryKey"`
	Enabled     bool  `json:"enabled" gorm:"not null;default:true"`
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
