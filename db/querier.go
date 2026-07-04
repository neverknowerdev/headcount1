package db

import (
	"context"

	"gorm.io/gorm"
)

type CompanyQuerier interface {
	CreateCompany(ctx context.Context, name string) (Company, error)
	ListCompanies(ctx context.Context) ([]Company, error)
	GetCompany(ctx context.Context, id int32) (Company, error)
}

type ProjectQuerier interface {
	CreateProject(ctx context.Context, p Project) (Project, error)
	GetProject(ctx context.Context, id int32) (Project, error)
	UpdateProject(ctx context.Context, p Project) (Project, error)
	ListProjectsByCompany(ctx context.Context, companyID int32) ([]Project, error)
}

type AgentQuerier interface {
	CreateAgent(ctx context.Context, a Agent) (Agent, error)
	ListAgentsByCompany(ctx context.Context, companyID int32) ([]Agent, error)
	GetAgent(ctx context.Context, id int32) (Agent, error)
	GetAgentWithCompany(ctx context.Context, id int32) (Agent, Company, error)
	UpdateAgent(ctx context.Context, a Agent) (Agent, error)
}

type TaskQuerier interface {
	CreateTask(ctx context.Context, t Task) (Task, error)
	UpdateTask(ctx context.Context, t Task) (Task, error)
	GetTask(ctx context.Context, id int32) (Task, error)
}

type SubtaskQuerier interface {
	ListSubtasksByParent(ctx context.Context, parentID int32) ([]Task, error)
	CountRunningSubtasks(ctx context.Context, parentID int32) (int64, error)
}

type CommentQuerier interface {
	CreateComment(ctx context.Context, c Comment) (Comment, error)
	ListCommentsByTask(ctx context.Context, taskID int32) ([]Comment, error)
}

type AttachmentQuerier interface {
	CreateAttachment(ctx context.Context, a Attachment) (Attachment, error)
	ListAttachmentsByTask(ctx context.Context, taskID int32) ([]Attachment, error)
}

type RunQuerier interface {
	CreateRun(ctx context.Context, r Run) (Run, error)
	UpdateRunLog(ctx context.Context, id int32, content string, status string) error
	UpdateRunSession(ctx context.Context, id int32, sessionID string) error
	GetRun(ctx context.Context, id int32) (Run, error)
	GetRunWithTask(ctx context.Context, runID int32) (Run, Task, error)
	GetRunBySessionID(ctx context.Context, sessionID string) (Run, error)
	ListChildRuns(ctx context.Context, parentRunID int32) ([]Run, error)
	UpdateRunCurrentStatus(ctx context.Context, id int32, status string) error
}

type LLMProviderQuerier interface {
	CreateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error)
	GetLLMProvider(ctx context.Context, id int32) (LLMProvider, error)
	ListLLMProviders(ctx context.Context) ([]LLMProvider, error)
	DeleteLLMProvider(ctx context.Context, id int32) error
	UpdateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error)
}

type SkillQuerier interface {
	CreateSkill(ctx context.Context, s Skill) (Skill, error)
}

type SprintQuerier interface {
	CreateSprint(ctx context.Context, s Sprint) (Sprint, error)
	ListSprintsByCompany(ctx context.Context, companyID int32) ([]Sprint, error)
}

type ProxyRequestLogQuerier interface {
	CreateProxyRequestLog(ctx context.Context, p ProxyRequestLog) (ProxyRequestLog, error)
}

type Querier interface {
	CompanyQuerier
	ProjectQuerier
	AgentQuerier
	TaskQuerier
	SubtaskQuerier
	CommentQuerier
	AttachmentQuerier
	RunQuerier
	LLMProviderQuerier
	SkillQuerier
	SprintQuerier
	ProxyRequestLogQuerier
}

type Queries struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Queries {
	return &Queries{db: db}
}
