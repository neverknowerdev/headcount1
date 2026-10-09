package db

import "agent-orchestrator/db/repository"

// Repository-owned values are re-exported from db during the migration so
// existing callers keep the stable db API while persistence is organized in
// db/repository.
type GitHubInstallationRecord = repository.GitHubInstallationRecord
type SaveGitHubOAuthAccountParams = repository.SaveGitHubOAuthAccountParams
type CodegraphProjectServer = repository.CodegraphProjectServer
type TeamMemberInfo = repository.TeamMemberInfo
type TransitionGuard = repository.TransitionGuard
type WorkflowTx = repository.WorkflowTx
type WorkflowChild = repository.WorkflowChild
type UsageFilter = repository.UsageFilter
type UsageTotals = repository.UsageTotals
type UsageGroup = repository.UsageGroup

const (
	PurposeCommitMessages = repository.PurposeCommitMessages
	PurposeSmart          = repository.PurposeSmart
	PurposeCheap          = repository.PurposeCheap
	PurposeClassifier     = repository.PurposeClassifier

	UsageByTask      = repository.UsageByTask
	UsageByRoot      = repository.UsageByRoot
	UsageByPhase     = repository.UsageByPhase
	UsageByAgent     = repository.UsageByAgent
	UsageByModel     = repository.UsageByModel
	UsageByTier      = repository.UsageByTier
	UsageByPhaseTier = repository.UsageByPhaseTier
	UsageByDay       = repository.UsageByDay

	ProviderNameOpenRouter     = repository.ProviderNameOpenRouter
	ProviderNameOpenCodeZen    = repository.ProviderNameOpenCodeZen
	OpenRouterBaseURL          = repository.OpenRouterBaseURL
	OpenCodeZenBaseURL         = repository.OpenCodeZenBaseURL
	ProviderVendorOpenRouter   = repository.ProviderVendorOpenRouter
	ProviderVendorOpenCodeZen  = repository.ProviderVendorOpenCodeZen
	ProviderPresetOpenCodeGo   = repository.ProviderPresetOpenCodeGo
	ProviderPresetMiniMax      = repository.ProviderPresetMiniMax
	ProviderPresetDeepSeek     = repository.ProviderPresetDeepSeek
	ProviderPresetTypeSafe     = repository.ProviderPresetTypeSafe
	ProviderPresetSurplus      = repository.ProviderPresetSurplus
	RunStatusPaused            = repository.RunStatusPaused
	RunStatusResuming          = repository.RunStatusResuming
	CheckpointVersion          = repository.CheckpointVersion
	AccessTokenLifetime        = repository.AccessTokenLifetime
	SessionLifetime            = repository.SessionLifetime
	PasswordResetTokenLifetime = repository.PasswordResetTokenLifetime
	TeamInviteLifetime         = repository.TeamInviteLifetime
	WebAuthnChallengeLifetime  = repository.WebAuthnChallengeLifetime
)

var (
	ErrRefreshReuse                   = repository.ErrRefreshReuse
	ErrGitHubIdentityAlreadyConnected = repository.ErrGitHubIdentityAlreadyConnected
	ErrGitHubWebhookAlreadyProcessing = repository.ErrGitHubWebhookAlreadyProcessing
	ErrGitHubWebhookLeaseLost         = repository.ErrGitHubWebhookLeaseLost
	NormalizeEmail                    = repository.NormalizeEmail
	SessionAbsoluteCap                = repository.SessionAbsoluteCap
	SessionReauthGap                  = repository.SessionReauthGap
	TaskGitBranch                     = repository.TaskGitBranch
	ExpandModelGroupMembers           = repository.ExpandModelGroupMembers
	ErrLeaseLost                      = repository.ErrLeaseLost
	ErrTransitionConflict             = repository.ErrTransitionConflict
	GuardFor                          = repository.GuardFor
)
