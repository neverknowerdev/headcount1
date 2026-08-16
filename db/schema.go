package db

import "gorm.io/gorm"

// EnsureSchema creates the current application schema for a database that does
// not already have it. Existing production databases are provisioned with the
// current schema and are not upgraded by application startup.
func EnsureSchema(database *gorm.DB) error {
	return database.AutoMigrate(
		&User{},
		&WebAuthnCredential{},
		&WebAuthnSession{},
		&Team{},
		&TeamMember{},
		&TeamInvite{},
		&Session{},
		&RefreshToken{},
		&UserGitCredential{},
		&PasswordResetToken{},
		&Company{},
		&Project{},
		&GitHubOAuthState{},
		&GitHubConnection{},
		&GitHubIdentity{},
		&GitHubWebhookDelivery{},
		&GitHubWebhookTarget{},
		&Sprint{},
		&LLMProvider{},
		&ModelGroup{},
		&ModelGroupMember{},
		&ModelRequestStat{},
		&DefaultModelSetting{},
		&ProviderPreset{},
		&Agent{},
		&Skill{},
		&Task{},
		&TaskRelation{},
		&Comment{},
		&Attachment{},
		&Run{},
		&RunStatusReport{},
		&RunEvent{},
		&Artifact{},
		&ActivityLog{},
		&ProxyRequestLog{},
		&MCPServer{},
		&MCPAccount{},
		&AgentMCPServer{},
		&AgentMCPAccount{},
		&MCPToolStat{},
		&AgentMCPToolFilter{},
	)
}
