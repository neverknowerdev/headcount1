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
	IsExternal      bool      `json:"is_external" gorm:"not null;default:false"`
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
	TaskTypeTech    = "tech"
	TaskTypeWriting = "writing"
	TaskTypeDesign  = "design"
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
	TaskType        string     `json:"task_type" gorm:"not null;default:'tech'"`
	Description     string     `json:"description"`
	Priority        string     `json:"priority" gorm:"not null;default:'Normal'"`
	Status          string     `json:"status" gorm:"not null;default:'backlog'"`
	DueDate         *time.Time `json:"due_date"`
	IsArchived      bool       `json:"is_archived" gorm:"not null;default:false"`
	RunID           *int32     `json:"run_id"`
	AgentConfigName    string     `json:"agent_config_name" gorm:"default:''"`
	MainSessionID      *int32     `json:"main_session_id"`
	UserInput          string     `json:"user_input" gorm:"type:text"`
	DetailedDescription string    `json:"detailed_description" gorm:"type:text"`
	Specifications     string     `json:"specifications" gorm:"type:text"`
	AcceptanceCriteria string     `json:"acceptance_criteria" gorm:"type:text"`
	TestCases          string     `json:"test_cases" gorm:"type:text"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type Session struct {
	ID              int32     `json:"id" gorm:"primaryKey"`
	TaskID          int32     `json:"task_id" gorm:"not null;index"`
	Task            Task      `json:"task,omitempty" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	ParentSessionID *int32    `json:"parent_session_id" gorm:"index"`
	Parent          *Session  `json:"parent,omitempty" gorm:"foreignKey:ParentSessionID;constraint:OnDelete:CASCADE;"`
	SessionType     string    `json:"session_type"` // "orchestration" | "qa-research" | "implementation" | "testing"
	AgentConfigName string    `json:"agent_config_name"`
	Status          string    `json:"status" gorm:"default:'queued'"` // "queued" | "running" | "waiting_for_answer" | "completed" | "failed"
	MessageHistory  string    `json:"message_history" gorm:"type:text"`
	ResultSummary   string    `json:"result_summary" gorm:"type:text"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type PendingQuestion struct {
	ID            int32     `json:"id" gorm:"primaryKey"`
	TaskID        int32     `json:"task_id" gorm:"not null;index"`
	FromSessionID int32     `json:"from_session_id" gorm:"not null"`
	ToSessionID   int32     `json:"to_session_id" gorm:"not null"`
	Question      string    `json:"question" gorm:"type:text"`
	Answer        string    `json:"answer" gorm:"type:text"`
	Status        string    `json:"status" gorm:"default:'pending'"` // "pending" | "answered"
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Comment struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	TaskID      int32     `json:"task_id" gorm:"not null"`
	Task        Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AuthorType  string    `json:"author_type" gorm:"not null"`
	AuthorID    *int32    `json:"author_id"`
	Content     string    `json:"content" gorm:"not null"`
	CommentType string    `json:"comment_type" gorm:"default:''"`
	RunID       *int32    `json:"run_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
	ID                int32      `json:"id" gorm:"primaryKey"`
	TaskID            int32      `json:"task_id" gorm:"not null"`
	Task              Task       `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AgentID           int32      `json:"agent_id" gorm:"not null"`
	Agent             Agent      `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:CASCADE;"`
	Status            string     `json:"status" gorm:"not null"`
	SessionID         string     `json:"session_id"`
	LogFilePath       string     `json:"log_file_path"`
	LogContent        string     `json:"log_content"`
	LogEntries        string     `json:"log_entries" gorm:"type:text"`        // JSON array of structured log entries
	TokenStats        string     `json:"token_stats" gorm:"type:text"`         // JSON object with aggregated token counts
	ResultDescription string     `json:"result_description" gorm:"type:text"` // short summary set by finish_task_execution
	ResultExplanation string     `json:"result_explanation" gorm:"type:text"` // detailed explanation set by finish_task_execution
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at"`
	LastMessageTime   *time.Time `json:"last_message_time"`
}

// RunTokenStats holds aggregated token counts for a run. Persisted to
// Run.TokenStats as JSON so the Run Logs UI can render an overall
// breakdown without re-iterating LogEntries on every read.
type RunTokenStats struct {
	PromptTokens     int            `json:"prompt_tokens"`               // sum of all LLM request input tokens (provider-reported)
	CompletionTokens int            `json:"completion_tokens"`           // sum of all LLM response output tokens (provider-reported)
	ReasoningTokens  int            `json:"reasoning_tokens"`            // sum of reasoning tokens (provider-reported, or estimated)
	ToolInputTokens  int            `json:"tool_input_tokens"`           // sum of tool call argument sizes (estimated, chars/4)
	ToolOutputTokens int            `json:"tool_output_tokens"`          // sum of tool response sizes (estimated, chars/4)
	CachedTokens     int            `json:"cached_tokens"`               // sum of cached prompt tokens (subset of PromptTokens)
	TotalTokens      int            `json:"total_tokens"`                // sum of everything above (excludes CachedTokens)
	MCPToolTokens    int            `json:"mcp_tool_tokens,omitempty"`   // subset of ToolOutputTokens from MCP dispatcher calls
	MCPServerTokens  map[string]int `json:"mcp_server_tokens,omitempty"` // per-server breakdown of MCPToolTokens
}

// IsEmpty reports whether all fields are zero, safe for use with map fields.
func (s RunTokenStats) IsEmpty() bool {
	return s.PromptTokens == 0 && s.CompletionTokens == 0 && s.ReasoningTokens == 0 &&
		s.ToolInputTokens == 0 && s.ToolOutputTokens == 0 && s.CachedTokens == 0 &&
		s.MCPToolTokens == 0 && len(s.MCPServerTokens) == 0
}

// MCPServer stores configuration for an MCP (Model Context Protocol) server.
// MCP servers are global (not company-scoped), like LLM providers.
type MCPServer struct {
	ID            int32        `json:"id" gorm:"primaryKey"`
	Name          string       `json:"name" gorm:"not null;uniqueIndex"` // unique slug, e.g. "github"
	DisplayName   string       `json:"display_name"`
	Description   string       `json:"description"`
	Transport     string       `json:"transport" gorm:"not null"` // "stdio", "http", "builtin"
	Command       string       `json:"command"`
	Args          string       `json:"args" gorm:"type:text"`
	URL           string       `json:"url"`
	Headers       string       `json:"headers" gorm:"type:text"`
	AuthType      string       `json:"auth_type"`                         // "none", "bearer", "credentials-file"
	AuthToken     string       `json:"-" gorm:"column:auth_token;type:text"` // legacy; migrated to MCPAccount on startup
	AuthEnvVar    string       `json:"auth_env_var"`
	ToolsCache    string       `json:"tools_cache" gorm:"type:text"`
	LastError     string       `json:"last_error" gorm:"type:text"`
	InitStatus    string       `json:"init_status" gorm:"default:''"` // codegraph lifecycle: "initializing", "ready", "error: ..."
	DepsInstalled  bool         `json:"deps_installed" gorm:"-"` // computed at runtime
	Deps           string       `json:"deps" gorm:"type:text"`   // JSON array of npm packages to pre-install, e.g. ["@modelcontextprotocol/server-gdrive"]
	Enabled        bool         `json:"enabled" gorm:"not null;default:true"`
	Builtin        bool         `json:"builtin" gorm:"not null;default:false"`
	WorkDir        string       `json:"work_dir"` // working directory for stdio servers (e.g. project repo path)
	ProjectID      *int32       `json:"project_id" gorm:"index"`
	Project        *Project     `json:"project,omitempty" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	Accounts      []MCPAccount `json:"accounts,omitempty" gorm:"foreignKey:MCPServerID"`
	Agents        []Agent      `json:"agents,omitempty" gorm:"many2many:agent_mcp_servers;"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// MCPAccount holds credentials for one identity on an MCPServer.
// A server can have multiple accounts (e.g., personal + work GitHub tokens).
type MCPAccount struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	MCPServerID int32     `json:"mcp_server_id" gorm:"not null;index"`
	Name        string    `json:"name" gorm:"not null"` // user label: "Personal", "Work"
	AuthToken   string    `json:"-" gorm:"type:text"`   // credential; never sent to clients
	HasToken    bool      `json:"has_token" gorm:"-"`   // computed: AuthToken != ""
	LastError   string    `json:"last_error" gorm:"type:text"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentMCPServer is the legacy join table for the Agent <-> MCPServer many-to-many.
// Still used for built-in (paperclip2) assignments.
type AgentMCPServer struct {
	AgentID     int32 `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32 `json:"mcp_server_id" gorm:"primaryKey"`
	Enabled     bool  `json:"enabled" gorm:"not null;default:true"`
}

// AgentMCPAccount is the join table for Agent <-> MCPAccount.
// External MCP servers are assigned at the account level (not server level).
type AgentMCPAccount struct {
	AgentID      int32 `json:"agent_id" gorm:"primaryKey"`
	MCPAccountID int32 `json:"mcp_account_id" gorm:"primaryKey"`
	Enabled      bool  `json:"enabled" gorm:"not null;default:true"`
}

// MCPToolStat tracks cumulative call counts per tool per MCP server.
// Used to sort tools by popularity in the agent system prompt.
type MCPToolStat struct {
	ID          int32  `json:"id" gorm:"primaryKey;autoIncrement"`
	MCPServerID int32  `json:"mcp_server_id" gorm:"not null;uniqueIndex:idx_mcp_tool_stat"`
	ToolName    string `json:"tool_name" gorm:"not null;uniqueIndex:idx_mcp_tool_stat"`
	CallCount   int64  `json:"call_count" gorm:"not null;default:0"`
}

// AgentMCPToolFilter stores per-agent, per-server tool enable/disable settings.
// When no row exists for a (agent, server, tool) triple, the tool is enabled by default.
type AgentMCPToolFilter struct {
	AgentID     int32  `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32  `json:"mcp_server_id" gorm:"primaryKey"`
	ToolName    string `json:"tool_name" gorm:"primaryKey"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`
}

type Artifact struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	CompanyID *int32    `json:"company_id" gorm:"index"`
	Company   *Company  `json:"company,omitempty" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID *int32    `json:"project_id" gorm:"index"`
	Project   *Project  `json:"project,omitempty" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	TaskID    int32     `json:"task_id" gorm:"not null"`
	Task      Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	RunID     int32     `json:"run_id" gorm:"not null"`
	Run       Run       `json:"run" gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE;"`
	Filename  string    `json:"filename" gorm:"not null"`
	FilePath  string    `json:"file_path" gorm:"not null"`
	Content   string    `json:"content" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
