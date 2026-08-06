package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

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

func (q *Queries) ListGitHubConnectionsForUser(ctx context.Context, userID int32) ([]GitHubConnection, error) {
	var connections []GitHubConnection
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").Find(&connections).Error
	return connections, err
}

func (q *Queries) CountGitHubAccountsForUser(ctx context.Context, serverID, userID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&MCPAccount{}).
		Where("mcp_server_id = ? AND user_id = ?", serverID, userID).
		Count(&count).Error
	return count, err
}

func (q *Queries) GetGitHubAccountForUser(ctx context.Context, accountID, serverID, userID int32) (MCPAccount, error) {
	var account MCPAccount
	err := q.db.WithContext(ctx).
		Where("id = ? AND mcp_server_id = ? AND user_id = ?", accountID, serverID, userID).
		First(&account).Error
	return account, err
}

func (q *Queries) GetBuiltinGitHubServer(ctx context.Context, serverID int32) (MCPServer, error) {
	var server MCPServer
	err := q.db.WithContext(ctx).
		Where("id = ? AND name = ? AND auth_type = ? AND builtin = ?", serverID, MCPServerNameGitHub, MCPAuthTypeGitHubApp, true).
		First(&server).Error
	return server, err
}

func (q *Queries) ListGitHubAccountsForUser(ctx context.Context, userID int32) ([]MCPAccount, error) {
	var accounts []MCPAccount
	err := q.db.WithContext(ctx).
		Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").
		Where("mcp_accounts.user_id = ? AND mcp_servers.name = ? AND mcp_servers.auth_type = ? AND mcp_servers.builtin = ?", userID, MCPServerNameGitHub, MCPAuthTypeGitHubApp, true).
		Find(&accounts).Error
	return accounts, err
}

func (q *Queries) GetGitHubAccountByIDForUser(ctx context.Context, accountID, userID int32) (MCPAccount, error) {
	var account MCPAccount
	err := q.db.WithContext(ctx).
		Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").
		Where("mcp_accounts.id = ? AND mcp_accounts.user_id = ? AND mcp_servers.name = ? AND mcp_servers.auth_type = ? AND mcp_servers.builtin = ?", accountID, userID, MCPServerNameGitHub, MCPAuthTypeGitHubApp, true).
		First(&account).Error
	return account, err
}

func (q *Queries) HasGitHubIdentity(ctx context.Context, account MCPAccount, userID int32) (bool, error) {
	if account.UserID == nil || *account.UserID != userID {
		return false, nil
	}
	var count int64
	err := q.db.WithContext(ctx).Model(&GitHubIdentity{}).
		Where("mcp_account_id = ? AND mcp_server_id = ? AND user_id = ?", account.ID, account.MCPServerID, userID).
		Count(&count).Error
	return count == 1, err
}

func (q *Queries) CreateGitHubOAuthState(ctx context.Context, state GitHubOAuthState) error {
	return q.db.WithContext(ctx).Create(&state).Error
}

func (q *Queries) GetGitHubOAuthState(ctx context.Context, stateID string) (GitHubOAuthState, error) {
	var state GitHubOAuthState
	err := q.db.WithContext(ctx).First(&state, "id = ?", stateID).Error
	return state, err
}

func (q *Queries) ClaimGitHubOAuthState(ctx context.Context, stateID string, now time.Time) (bool, error) {
	claim := q.db.WithContext(ctx).Model(&GitHubOAuthState{}).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", stateID, now).
		Update("used_at", now)
	return claim.RowsAffected == 1, claim.Error
}

func (q *Queries) SaveGitHubOAuthAccount(ctx context.Context, params SaveGitHubOAuthAccountParams) (MCPAccount, error) {
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
	err := tx.Where("id = ? AND mcp_server_id = ? AND user_id = ?", state.MCPAccountID, state.MCPServerID, state.UserID).
		First(&account).Error
	return account, err
}

func rejectDuplicateGitHubIdentity(tx *gorm.DB, state GitHubOAuthState, githubUserID int64) error {
	var existing GitHubIdentity
	err := tx.Where("mcp_server_id = ? AND user_id = ? AND git_hub_user_id = ?", state.MCPServerID, state.UserID, githubUserID).
		First(&existing).Error
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
		account = MCPAccount{
			MCPServerID:        params.State.MCPServerID,
			Name:               params.GitHubLogin,
			AuthTokenEncrypted: params.SealedToken,
			UserID:             &params.State.UserID,
		}
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
		identity = GitHubIdentity{
			MCPAccountID: accountID,
			MCPServerID:  params.State.MCPServerID,
			UserID:       params.State.UserID,
			GitHubUserID: params.GitHubUserID,
			GitHubLogin:  params.GitHubLogin,
		}
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
		connection := GitHubConnection{
			InstallationID: installation.InstallationID,
			MCPAccountID:   accountID,
			UserID:         params.State.UserID,
			AccountLogin:   installation.AccountLogin,
			ConnectedAt:    params.ConnectedAt,
		}
		if err := tx.Create(&connection).Error; err != nil {
			return err
		}
	}
	return nil
}

func (q *Queries) UpsertGitHubConnection(ctx context.Context, connection GitHubConnection) error {
	return q.db.WithContext(ctx).
		Where("mcp_account_id = ? AND installation_id = ?", connection.MCPAccountID, connection.InstallationID).
		Assign(connection).FirstOrCreate(&connection).Error
}

func (q *Queries) ClaimGitHubWebhookDelivery(ctx context.Context, deliveryID, event, attemptToken string, now, leaseUntil time.Time) (GitHubWebhookDelivery, bool, error) {
	var delivery GitHubWebhookDelivery
	err := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		delivery = GitHubWebhookDelivery{DeliveryID: deliveryID, Event: event, Status: "pending"}
		if createErr := q.db.WithContext(ctx).Create(&delivery).Error; createErr != nil {
			if loadErr := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; loadErr != nil {
				return GitHubWebhookDelivery{}, false, createErr
			}
		}
	} else if err != nil {
		return GitHubWebhookDelivery{}, false, err
	}
	if delivery.Status == "completed" {
		return delivery, true, nil
	}
	claim := q.db.WithContext(ctx).Model(&GitHubWebhookDelivery{}).
		Where("delivery_id = ? AND status <> ? AND (status <> ? OR lease_expires_at IS NULL OR lease_expires_at <= ?)", deliveryID, "completed", "processing", now).
		Updates(map[string]any{"status": "processing", "attempt_token": attemptToken, "lease_expires_at": &leaseUntil, "attempts": gorm.Expr("attempts + 1"), "last_error": ""})
	if claim.Error != nil {
		return GitHubWebhookDelivery{}, false, claim.Error
	}
	if claim.RowsAffected == 0 {
		if err := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
			return GitHubWebhookDelivery{}, false, err
		}
		if delivery.Status == "completed" {
			return delivery, true, nil
		}
		return GitHubWebhookDelivery{}, false, ErrGitHubWebhookAlreadyProcessing
	}
	if err := q.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
		return GitHubWebhookDelivery{}, false, err
	}
	return delivery, false, nil
}

func (q *Queries) UpdateGitHubWebhookDelivery(ctx context.Context, deliveryID, attemptToken string, values map[string]any) error {
	result := q.db.WithContext(ctx).Model(&GitHubWebhookDelivery{}).
		Where("delivery_id = ? AND attempt_token = ?", deliveryID, attemptToken).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHubWebhookLeaseLost
	}
	return nil
}

func (q *Queries) ListPendingGitHubWebhookTargets(ctx context.Context, deliveryID string) ([]GitHubWebhookTarget, error) {
	var targets []GitHubWebhookTarget
	err := q.db.WithContext(ctx).Where("delivery_id = ? AND wake_status <> ?", deliveryID, "completed").Find(&targets).Error
	return targets, err
}

func (q *Queries) ClaimGitHubWebhookTarget(ctx context.Context, targetID int32, attemptToken string, now, leaseUntil time.Time) (bool, error) {
	claim := q.db.WithContext(ctx).Model(&GitHubWebhookTarget{}).
		Where("id = ? AND wake_status <> ? AND (wake_status <> ? OR wake_lease_expires_at IS NULL OR wake_lease_expires_at <= ?)", targetID, "completed", "processing", now).
		Updates(map[string]any{"wake_status": "processing", "wake_attempt_token": attemptToken, "wake_lease_expires_at": &leaseUntil, "wake_attempts": gorm.Expr("wake_attempts + 1"), "wake_last_error": ""})
	return claim.RowsAffected == 1, claim.Error
}

func (q *Queries) UpdateGitHubWebhookTarget(ctx context.Context, targetID int32, attemptToken string, values map[string]any) error {
	result := q.db.WithContext(ctx).Model(&GitHubWebhookTarget{}).
		Where("id = ? AND wake_attempt_token = ?", targetID, attemptToken).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrGitHubWebhookLeaseLost
	}
	return nil
}

func (q *Queries) CountPendingGitHubWebhookTargets(ctx context.Context, deliveryID string) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&GitHubWebhookTarget{}).
		Where("delivery_id = ? AND wake_status <> ?", deliveryID, "completed").Count(&count).Error
	return count, err
}

func (q *Queries) CreateGitHubWebhookComments(ctx context.Context, deliveryID string, repositoryID int64, pullRequestNumber int, content string) ([]Comment, error) {
	comments := []Comment{}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projects []Project
		if err := tx.Where("git_hub_repository_id = ?", repositoryID).Find(&projects).Error; err != nil {
			return fmt.Errorf("load projects for GitHub webhook: %w", err)
		}
		for _, project := range projects {
			var task Task
			err := tx.Where("project_id = ? AND git_hub_pr_number = ?", project.ID, pullRequestNumber).Order("id desc").First(&task).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("load task for GitHub webhook: %w", err)
			}
			var existing GitHubWebhookTarget
			err = tx.Where("delivery_id = ? AND task_id = ?", deliveryID, task.ID).First(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("load GitHub webhook target: %w", err)
			}
			comment := Comment{TaskID: task.ID, AuthorType: "github", CommentType: "github", Content: content}
			if err := tx.Create(&comment).Error; err != nil {
				return fmt.Errorf("save GitHub comment: %w", err)
			}
			if err := tx.Create(&GitHubWebhookTarget{DeliveryID: deliveryID, TaskID: task.ID, CommentID: comment.ID}).Error; err != nil {
				return fmt.Errorf("save GitHub webhook target: %w", err)
			}
			comments = append(comments, comment)
		}
		return nil
	})
	return comments, err
}
