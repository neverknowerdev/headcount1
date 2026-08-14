package db

import (
	"context"
	"time"

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

type TaskRelationQuerier interface {
	CreateTaskRelation(ctx context.Context, relation TaskRelation) (TaskRelation, error)
	DeleteTaskRelation(ctx context.Context, relationID int32) error
	GetTaskRelation(ctx context.Context, relationID int32) (TaskRelation, error)
	ListTaskRelations(ctx context.Context, taskID int32) ([]TaskRelation, error)
	ListBlockingDependencies(ctx context.Context, taskID int32) ([]Task, error)
	ListDependentTasks(ctx context.Context, prerequisiteTaskID int32) ([]Task, error)
	ListQueuedTasksForReconciliation(ctx context.Context) ([]Task, error)
	ListTaskRelationSummaries(ctx context.Context, taskIDs []int32) (map[int32]TaskRelationSummary, error)
	CanStartTask(ctx context.Context, taskID int32) (bool, []Task, error)
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
	GetLatestRunStatusReport(ctx context.Context, runID int32) (RunStatusReport, error)
	SetRunStatusRefreshRequestedAt(ctx context.Context, runID int32, at *time.Time) error
}

type LLMProviderQuerier interface {
	CreateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error)
	GetLLMProvider(ctx context.Context, id int32) (LLMProvider, error)
	ListLLMProviders(ctx context.Context) ([]LLMProvider, error)
	DeleteLLMProvider(ctx context.Context, id int32) error
	UpdateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error)
	EnsureBuiltinLLMProvidersForUser(ctx context.Context, userID int32) error
	UpdateLLMProviderModelCatalog(ctx context.Context, providerID int32, models []string) error
	ForceUpdateLLMProviderModelCatalog(ctx context.Context, providerID int32, models []string) error
}

type ProviderPresetQuerier interface {
	ListProviderPresets(ctx context.Context) ([]ProviderPreset, error)
	GetProviderPresetByKey(ctx context.Context, key string) (ProviderPreset, error)
	EnsureProviderPresets(ctx context.Context) error
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
	TaskRelationQuerier
	SubtaskQuerier
	CommentQuerier
	AttachmentQuerier
	RunQuerier
	LLMProviderQuerier
	ProviderPresetQuerier
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
