package db

import "agent-orchestrator/db/models"

// Model aliases preserve the stable db package API while persisted model
// definitions live in db/models. New code may import db/models directly.
type ActivityLog = models.ActivityLog
type Agent = models.Agent
type AgentMCPAccount = models.AgentMCPAccount
type AgentMCPServer = models.AgentMCPServer
type AgentMCPToolFilter = models.AgentMCPToolFilter
type Artifact = models.Artifact
type Attachment = models.Attachment
type CheckpointPhase = models.CheckpointPhase
type Comment = models.Comment
type Company = models.Company
type DefaultModelSetting = models.DefaultModelSetting
type GitHubConnection = models.GitHubConnection
type GitHubIdentity = models.GitHubIdentity
type GitHubOAuthState = models.GitHubOAuthState
type GitHubWebhookDelivery = models.GitHubWebhookDelivery
type GitHubWebhookTarget = models.GitHubWebhookTarget
type LLMProvider = models.LLMProvider
type MCPAccount = models.MCPAccount
type MCPServer = models.MCPServer
type MCPToolStat = models.MCPToolStat
type ModelGroup = models.ModelGroup
type ModelGroupMember = models.ModelGroupMember
type ModelRequestStat = models.ModelRequestStat
type PasswordResetToken = models.PasswordResetToken
type Project = models.Project
type ProviderPreset = models.ProviderPreset
type ProxyRequestLog = models.ProxyRequestLog
type RefreshToken = models.RefreshToken
type Run = models.Run
type RunEvent = models.RunEvent
type SessionMessage = models.SessionMessage
type WorkerFinishedMessage = models.WorkerFinishedMessage
type RunEventType = models.RunEventType
type RunRecovery = models.RunRecovery
type RunStatusReport = models.RunStatusReport
type RunTokenStats = models.RunTokenStats
type Session = models.Session
type Skill = models.Skill
type Sprint = models.Sprint
type Task = models.Task
type TaskRelation = models.TaskRelation
type TaskRelationTask = models.TaskRelationTask
type TaskRelationView = models.TaskRelationView
type TaskRelationSummary = models.TaskRelationSummary
type Team = models.Team
type TeamInvite = models.TeamInvite
type TeamMember = models.TeamMember
type User = models.User
type UserGitCredential = models.UserGitCredential
type WebAuthnCredential = models.WebAuthnCredential
type WebAuthnSession = models.WebAuthnSession

const CheckpointPhaseAfterTools = models.CheckpointPhaseAfterTools
const CheckpointPhaseBeforeTools = models.CheckpointPhaseBeforeTools
const DefaultTaskGitBaseBranch = models.DefaultTaskGitBaseBranch
const MCPAuthTypeGitHubApp = models.MCPAuthTypeGitHubApp
const MCPServerNameGitHub = models.MCPServerNameGitHub
const MCPTransportBuiltin = models.MCPTransportBuiltin
const RunEventTypeLifecycleStatus = models.RunEventTypeLifecycleStatus
const RunEventTypeStatusRefresh = models.RunEventTypeStatusRefresh
const RunEventTypeStatusReport = models.RunEventTypeStatusReport
const RunEventTypeWorkerQuestion = models.RunEventTypeWorkerQuestion
const RunEventTypeSessionMessage = models.RunEventTypeSessionMessage
const RunEventTypeSessionAnswer = models.RunEventTypeSessionAnswer
const RunEventTypeWorkerFinished = models.RunEventTypeWorkerFinished
const RunEventTypeHumanInputRequested = models.RunEventTypeHumanInputRequested
const RunEventTypeHumanInputAnswered = models.RunEventTypeHumanInputAnswered
const RunKindTaskOrchestrator = models.RunKindTaskOrchestrator
const RunKindAgentSession = models.RunKindAgentSession
const RunKindCEOConsultation = models.RunKindCEOConsultation
const RunKindHelperWorker = models.RunKindHelperWorker

var NewSessionMessage = models.NewSessionMessage

const TaskRelationDependsOn = models.TaskRelationDependsOn
const TaskRelationRelatedTo = models.TaskRelationRelatedTo
const TaskStatusBacklog = models.TaskStatusBacklog
const TaskStatusBlocked = models.TaskStatusBlocked
const TaskStatusDependsOnTask = models.TaskStatusDependsOnTask
const TaskStatusDone = models.TaskStatusDone
const TaskStatusInProgress = models.TaskStatusInProgress
const TaskStatusInReview = models.TaskStatusInReview
const TaskStatusTodo = models.TaskStatusTodo
const TaskStatusRefinement = models.TaskStatusRefinement
const TeamRoleMember = models.TeamRoleMember
const TeamRoleOwner = models.TeamRoleOwner

var ProviderSlug = models.ProviderSlug
