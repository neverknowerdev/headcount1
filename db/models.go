package db

import "agent-orchestrator/db/models"

// Model aliases preserve the stable db package API while persisted model
// definitions live in db/models. New code may import db/models directly.
type ActivityLog = models.ActivityLog
type Agent = models.Agent
type Artifact = models.Artifact
type Attachment = models.Attachment
type CheckpointPhase = models.CheckpointPhase
type Comment = models.Comment
type Company = models.Company
type Decision = models.Decision
type DefaultModelSetting = models.DefaultModelSetting
type GitHubConnection = models.GitHubConnection
type GitHubIdentity = models.GitHubIdentity
type GitHubOAuthState = models.GitHubOAuthState
type GitHubWebhookDelivery = models.GitHubWebhookDelivery
type GitHubWebhookTarget = models.GitHubWebhookTarget
type LLMCall = models.LLMCall
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
type RefreshToken = models.RefreshToken
type Run = models.Run
type RunRecovery = models.RunRecovery
type RunTokenStats = models.RunTokenStats
type Session = models.Session
type Skill = models.Skill
type Sprint = models.Sprint
type Task = models.Task
type TaskRelation = models.TaskRelation
type TaskStep = models.TaskStep
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

const TaskRelationDependsOn = models.TaskRelationDependsOn
const TaskRelationRelatedTo = models.TaskRelationRelatedTo
const TaskStatusBacklog = models.TaskStatusBacklog
const TaskStatusBlocked = models.TaskStatusBlocked
const TaskStatusCanceled = models.TaskStatusCanceled
const TaskStatusDependsOnTask = models.TaskStatusDependsOnTask
const TaskStatusDone = models.TaskStatusDone
const TaskStatusFailed = models.TaskStatusFailed
const TaskStatusInProgress = models.TaskStatusInProgress
const TaskStatusInReview = models.TaskStatusInReview
const TaskStatusTodo = models.TaskStatusTodo
const TaskTypeResearch = models.TaskTypeResearch
const TaskTypeCoding = models.TaskTypeCoding
const TaskTypeReview = models.TaskTypeReview
const TaskTypeGeneral = models.TaskTypeGeneral
const TaskModeManaged = models.TaskModeManaged
const TaskModeDirect = models.TaskModeDirect
const TeamRoleMember = models.TeamRoleMember
const TeamRoleOwner = models.TeamRoleOwner

var ProviderSlug = models.ProviderSlug
