package db

import (
	"agent-orchestrator/db/repository"
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
	DeleteAgent(ctx context.Context, id int32) error
	ListAgentsByCompany(ctx context.Context, companyID int32) ([]Agent, error)
	GetAgent(ctx context.Context, id int32) (Agent, error)
	GetAgentWithCompany(ctx context.Context, id int32) (Agent, Company, error)
	UpdateAgent(ctx context.Context, a Agent) (Agent, error)
	EnsureBuiltinAgentsForCompany(ctx context.Context, companyID int32, defaults []Agent, providerID *int32, model string) error
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
	RecordRunStatusReport(ctx context.Context, id int32, status string, messageID int64) error
	GetLatestRunStatusReport(ctx context.Context, runID int32) (RunStatusReport, error)
	ListRunStatusReports(ctx context.Context, runID int32) ([]RunStatusReport, error)
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
	*repository.AgentRepository
	*repository.ArtifactRepository
	*repository.AttachmentRepository
	*repository.ActivityLogRepository
	*repository.CommentRepository
	*repository.CompanyRepository
	*repository.DefaultModelSettingRepository
	*repository.GitHubConnectionRepository
	*repository.GitHubIdentityRepository
	*repository.GitHubOAuthStateRepository
	*repository.GitHubWebhookDeliveryRepository
	*repository.GitHubWebhookTargetRepository
	*repository.LLMProviderRepository
	*repository.MCPServerRepository
	*repository.MCPAccountRepository
	*repository.MCPToolStatRepository
	*repository.AgentMCPServerRepository
	*repository.AgentMCPAccountRepository
	*repository.AgentMCPToolFilterRepository
	*repository.ModelGroupRepository
	*repository.ModelGroupMemberRepository
	*repository.ModelRequestStatRepository
	*repository.PasswordResetTokenRepository
	*repository.ProjectRepository
	*repository.ProviderPresetRepository
	*repository.ProxyRequestLogRepository
	*repository.RefreshTokenRepository
	*repository.RunRepository
	*repository.RunEventRepository
	*repository.RunStatusReportRepository
	*repository.SessionRepository
	*repository.SkillRepository
	*repository.SprintRepository
	*repository.TaskRelationRepository
	*repository.TaskRepository
	*repository.TeamRepository
	*repository.TeamMemberRepository
	*repository.TeamInviteRepository
	*repository.UserGitCredentialRepository
	*repository.UserRepository
	*repository.WebAuthnCredentialRepository
	*repository.WebAuthnSessionRepository
}

func New(db *gorm.DB) *Queries {
	return &Queries{
		db:                              db,
		AgentRepository:                 repository.NewAgentRepository(db),
		ArtifactRepository:              repository.NewArtifactRepository(db),
		AttachmentRepository:            repository.NewAttachmentRepository(db),
		ActivityLogRepository:           repository.NewActivityLogRepository(db),
		CommentRepository:               repository.NewCommentRepository(db),
		CompanyRepository:               repository.NewCompanyRepository(db),
		DefaultModelSettingRepository:   repository.NewDefaultModelSettingRepository(db),
		GitHubConnectionRepository:      repository.NewGitHubConnectionRepository(db),
		GitHubIdentityRepository:        repository.NewGitHubIdentityRepository(db),
		GitHubOAuthStateRepository:      repository.NewGitHubOAuthStateRepository(db),
		GitHubWebhookDeliveryRepository: repository.NewGitHubWebhookDeliveryRepository(db),
		GitHubWebhookTargetRepository:   repository.NewGitHubWebhookTargetRepository(db),
		LLMProviderRepository:           repository.NewLLMProviderRepository(db),
		MCPServerRepository:             repository.NewMCPServerRepository(db),
		MCPAccountRepository:            repository.NewMCPAccountRepository(db),
		MCPToolStatRepository:           repository.NewMCPToolStatRepository(db),
		AgentMCPServerRepository:        repository.NewAgentMCPServerRepository(db),
		AgentMCPAccountRepository:       repository.NewAgentMCPAccountRepository(db),
		AgentMCPToolFilterRepository:    repository.NewAgentMCPToolFilterRepository(db),
		ModelGroupRepository:            repository.NewModelGroupRepository(db),
		ModelGroupMemberRepository:      repository.NewModelGroupMemberRepository(db),
		ModelRequestStatRepository:      repository.NewModelRequestStatRepository(db),
		PasswordResetTokenRepository:    repository.NewPasswordResetTokenRepository(db),
		ProjectRepository:               repository.NewProjectRepository(db),
		ProviderPresetRepository:        repository.NewProviderPresetRepository(db),
		ProxyRequestLogRepository:       repository.NewProxyRequestLogRepository(db),
		RefreshTokenRepository:          repository.NewRefreshTokenRepository(db),
		RunRepository:                   repository.NewRunRepository(db),
		RunEventRepository:              repository.NewRunEventRepository(db),
		RunStatusReportRepository:       repository.NewRunStatusReportRepository(db),
		SessionRepository:               repository.NewSessionRepository(db),
		SkillRepository:                 repository.NewSkillRepository(db),
		SprintRepository:                repository.NewSprintRepository(db),
		TaskRelationRepository:          repository.NewTaskRelationRepository(db),
		TaskRepository:                  repository.NewTaskRepository(db),
		TeamRepository:                  repository.NewTeamRepository(db),
		TeamMemberRepository:            repository.NewTeamMemberRepository(db),
		TeamInviteRepository:            repository.NewTeamInviteRepository(db),
		UserGitCredentialRepository:     repository.NewUserGitCredentialRepository(db),
		UserRepository:                  repository.NewUserRepository(db),
		WebAuthnCredentialRepository:    repository.NewWebAuthnCredentialRepository(db),
		WebAuthnSessionRepository:       repository.NewWebAuthnSessionRepository(db),
	}
}

// DeleteAllFromTable is an administrative backup/restore primitive rather
// than a table repository operation; callers supply the validated table name.
func (q *Queries) DeleteAllFromTable(ctx context.Context, tableName string) error {
	return q.db.WithContext(ctx).Exec("DELETE FROM " + tableName).Error
}
