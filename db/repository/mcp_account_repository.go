package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

type MCPAccountRepository struct{ db *gorm.DB }

func NewMCPAccountRepository(db *gorm.DB) *MCPAccountRepository {
	return &MCPAccountRepository{db: db}
}

var (
	ErrGitHubIdentityAlreadyConnected = errors.New("GitHub identity is already connected")
	ErrGitHubWebhookAlreadyProcessing = errors.New("GitHub webhook delivery is already processing")
	ErrGitHubWebhookLeaseLost         = errors.New("GitHub webhook delivery lease was lost")
)

type GitHubInstallationRecord struct {
	InstallationID int64
	AccountLogin   string
}
type SaveGitHubOAuthAccountParams struct {
	State         GitHubOAuthState
	GitHubUserID  int64
	GitHubLogin   string
	SealedToken   string
	Installations []GitHubInstallationRecord
	ConnectedAt   time.Time
}

func (q *MCPAccountRepository) CountGitHubAccountsForUser(ctx context.Context, serverID, userID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&MCPAccount{}).Where("mcp_server_id = ? AND user_id = ?", serverID, userID).Count(&count).Error
	return count, err
}
func (q *MCPAccountRepository) GetGitHubAccountForUser(ctx context.Context, accountID, serverID, userID int32) (MCPAccount, error) {
	var account MCPAccount
	err := q.db.WithContext(ctx).Where("id = ? AND mcp_server_id = ? AND user_id = ?", accountID, serverID, userID).First(&account).Error
	return account, err
}
func (q *MCPAccountRepository) GetBuiltinGitHubServer(ctx context.Context, serverID int32) (MCPServer, error) {
	var server MCPServer
	err := q.db.WithContext(ctx).Where("id = ? AND name = ? AND auth_type = ? AND builtin = ?", serverID, MCPServerNameGitHub, MCPAuthTypeGitHubApp, true).First(&server).Error
	return server, err
}
func (q *MCPAccountRepository) ListGitHubAccountsForUser(ctx context.Context, userID int32) ([]MCPAccount, error) {
	var accounts []MCPAccount
	err := q.db.WithContext(ctx).Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").Where("mcp_accounts.user_id = ? AND mcp_servers.name = ? AND mcp_servers.auth_type = ? AND mcp_servers.builtin = ?", userID, MCPServerNameGitHub, MCPAuthTypeGitHubApp, true).Find(&accounts).Error
	return accounts, err
}
func (q *MCPAccountRepository) GetGitHubAccountByIDForUser(ctx context.Context, accountID, userID int32) (MCPAccount, error) {
	var account MCPAccount
	err := q.db.WithContext(ctx).Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").Where("mcp_accounts.id = ? AND mcp_accounts.user_id = ? AND mcp_servers.name = ? AND mcp_servers.auth_type = ? AND mcp_servers.builtin = ?", accountID, userID, MCPServerNameGitHub, MCPAuthTypeGitHubApp, true).First(&account).Error
	return account, err
}
func (q *MCPAccountRepository) SaveGitHubOAuthAccount(ctx context.Context, params SaveGitHubOAuthAccountParams) (MCPAccount, error) {
	var saved MCPAccount
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account, err := loadGitHubOAuthAccount(tx, params.State)
		if err != nil {
			return err
		}
		if err := rejectDuplicateGitHubIdentity(tx, params.State, params.GitHubUserID); err != nil {
			return err
		}
		account, err = saveGitHubMCPAccount(tx, account, params)
		if err != nil {
			return err
		}
		if err := saveGitHubIdentity(tx, account.ID, params); err != nil {
			return err
		}
		if err := replaceGitHubConnections(tx, account.ID, params); err != nil {
			return err
		}
		saved = account
		return nil
	})
	return saved, err
}
func loadGitHubOAuthAccount(tx *gorm.DB, state GitHubOAuthState) (MCPAccount, error) {
	if state.MCPAccountID == 0 {
		return MCPAccount{}, nil
	}
	var account MCPAccount
	err := tx.Where("id = ? AND mcp_server_id = ? AND user_id = ?", state.MCPAccountID, state.MCPServerID, state.UserID).First(&account).Error
	return account, err
}
func rejectDuplicateGitHubIdentity(tx *gorm.DB, state GitHubOAuthState, githubUserID int64) error {
	var existing GitHubIdentity
	err := tx.Where("mcp_server_id = ? AND user_id = ? AND git_hub_user_id = ?", state.MCPServerID, state.UserID, githubUserID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil
	case err != nil:
		return err
	case existing.MCPAccountID != state.MCPAccountID:
		return ErrGitHubIdentityAlreadyConnected
	default:
		return nil
	}
}
func saveGitHubMCPAccount(tx *gorm.DB, account MCPAccount, params SaveGitHubOAuthAccountParams) (MCPAccount, error) {
	if params.State.MCPAccountID == 0 {
		account = MCPAccount{MCPServerID: params.State.MCPServerID, Name: params.GitHubLogin, AuthTokenEncrypted: params.SealedToken, UserID: &params.State.UserID}
		return account, tx.Create(&account).Error
	}
	account.Name = params.GitHubLogin
	account.AuthTokenEncrypted = params.SealedToken
	account.LastError = ""
	return account, tx.Save(&account).Error
}
func saveGitHubIdentity(tx *gorm.DB, accountID int32, params SaveGitHubOAuthAccountParams) error {
	var identity GitHubIdentity
	err := tx.Where("mcp_account_id = ?", accountID).First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		identity = GitHubIdentity{MCPAccountID: accountID, MCPServerID: params.State.MCPServerID, UserID: params.State.UserID, GitHubUserID: params.GitHubUserID, GitHubLogin: params.GitHubLogin}
		if err := tx.Create(&identity).Error; err != nil {
			return normalizeGitHubIdentityError(err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	identity.MCPServerID = params.State.MCPServerID
	identity.UserID = params.State.UserID
	identity.GitHubUserID = params.GitHubUserID
	identity.GitHubLogin = params.GitHubLogin
	if err := tx.Save(&identity).Error; err != nil {
		return normalizeGitHubIdentityError(err)
	}
	return nil
}
func normalizeGitHubIdentityError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrGitHubIdentityAlreadyConnected
	}
	return err
}
func replaceGitHubConnections(tx *gorm.DB, accountID int32, params SaveGitHubOAuthAccountParams) error {
	if err := tx.Where("mcp_account_id = ?", accountID).Delete(&GitHubConnection{}).Error; err != nil {
		return err
	}
	for _, installation := range params.Installations {
		connection := GitHubConnection{InstallationID: installation.InstallationID, MCPAccountID: accountID, UserID: params.State.UserID, AccountLogin: installation.AccountLogin, ConnectedAt: params.ConnectedAt}
		if err := tx.Create(&connection).Error; err != nil {
			return err
		}
	}
	return nil
}
func (q *MCPAccountRepository) CreateMCPAccount(ctx context.Context, a MCPAccount) (MCPAccount, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	a.HasToken = a.AuthTokenEncrypted != ""
	return a, err
}
func (q *MCPAccountRepository) GetMCPAccount(ctx context.Context, id int32) (MCPAccount, error) {
	var a MCPAccount
	err := q.db.WithContext(ctx).First(&a, id).Error
	a.HasToken = a.AuthTokenEncrypted != ""
	return a, err
}
func (q *MCPAccountRepository) UpdateMCPAccount(ctx context.Context, a MCPAccount) (MCPAccount, error) {
	err := q.db.WithContext(ctx).Save(&a).Error
	a.HasToken = a.AuthTokenEncrypted != ""
	return a, err
}
func (q *MCPAccountRepository) DeleteMCPAccount(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("mcp_account_id = ?", id).Delete(&AgentMCPAccount{}).Error; err != nil {
			return err
		}
		if err := tx.Where("mcp_account_id = ?", id).Delete(&GitHubConnection{}).Error; err != nil {
			return err
		}
		if err := tx.Where("mcp_account_id = ?", id).Delete(&GitHubIdentity{}).Error; err != nil {
			return err
		}
		return tx.Delete(&MCPAccount{}, id).Error
	})
}
func (q *MCPAccountRepository) UpdateMCPAccountLastError(ctx context.Context, id int32, errMsg string) error {
	return q.db.WithContext(ctx).Model(&MCPAccount{}).Where("id = ?", id).Update("last_error", errMsg).Error
}
func (q *MCPAccountRepository) ListMCPAccountsForServer(ctx context.Context, serverID int32) ([]MCPAccount, error) {
	var accounts []MCPAccount
	err := q.db.WithContext(ctx).Where("mcp_server_id = ?", serverID).Find(&accounts).Error
	for i := range accounts {
		accounts[i].HasToken = accounts[i].AuthTokenEncrypted != ""
	}
	return accounts, err
}
func (q *MCPAccountRepository) ListMCPAccountsForAgent(ctx context.Context, agentID int32) ([]MCPAccount, error) {
	var accounts []MCPAccount
	err := q.db.WithContext(ctx).Joins("JOIN agent_mcp_accounts ON agent_mcp_accounts.mcp_account_id = mcp_accounts.id").Where("agent_mcp_accounts.agent_id = ? AND agent_mcp_accounts.enabled = ?", agentID, true).Find(&accounts).Error
	return accounts, err
}
