package db

import "agent-orchestrator/db/repository"

// Repository-owned values are re-exported from db for compatibility so
// existing callers keep the stable db API while persistence is organized in
// db/repository.
type GitHubInstallationRecord = repository.GitHubInstallationRecord
type SaveGitHubOAuthAccountParams = repository.SaveGitHubOAuthAccountParams
type CodegraphProjectServer = repository.CodegraphProjectServer
type TeamMemberInfo = repository.TeamMemberInfo

const (
	PurposeCommitMessages   = repository.PurposeCommitMessages
	PurposeAskArtifact      = repository.PurposeAskArtifact
	PurposeTaskOrchestrator = repository.PurposeTaskOrchestrator

	ProviderNameOpenRouter     = repository.ProviderNameOpenRouter
	ProviderNameOpenCodeZen    = repository.ProviderNameOpenCodeZen
	OpenRouterBaseURL          = repository.OpenRouterBaseURL
	OpenCodeZenBaseURL         = repository.OpenCodeZenBaseURL
	ProviderVendorOpenRouter   = repository.ProviderVendorOpenRouter
	ProviderVendorOpenCodeZen  = repository.ProviderVendorOpenCodeZen
	ProviderPresetOpenCodeGo   = repository.ProviderPresetOpenCodeGo
	ProviderPresetMiniMax      = repository.ProviderPresetMiniMax
	ProviderPresetDeepSeek     = repository.ProviderPresetDeepSeek
	RunStatusPaused            = repository.RunStatusPaused
	RunStatusRecoverableFailed = repository.RunStatusRecoverableFailed
	RunStatusStale             = repository.RunStatusStale
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
)
