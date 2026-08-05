package endpoints

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/githubapp"
	"agent-orchestrator/pkg/secrets"
	"gorm.io/gorm"
)

func githubClient() (*githubapp.Client, error) { return githubapp.FromEnv() }
func deploymentURL() string                    { return strings.TrimRight(os.Getenv("DEPLOY_URL"), "/") }

const forwardedWebhookSignatureHeader = "X-Headcount1-Webhook-Forward-Signature"

var errGitHubIdentityAlreadyConnected = errors.New("GitHub identity is already connected")

func githubOAuthStateBelongsToUser(state db.GitHubOAuthState, userID int32) bool {
	return userID != 0 && state.UserID != 0 && state.UserID == userID
}

// hasValidGitHubIdentity ensures the generic MCP account is still bound to the
// durable OAuth identity that was verified at callback time. In particular,
// this keeps disabled legacy/duplicate rows from being resurrected merely
// because they still contain an encrypted token.
func (api *API) hasValidGitHubIdentity(ctx context.Context, account db.MCPAccount, userID int32) (bool, error) {
	if account.UserID == nil || *account.UserID != userID {
		return false, nil
	}
	var count int64
	err := api.db.WithContext(ctx).Model(&db.GitHubIdentity{}).
		Where("mcp_account_id = ? AND mcp_server_id = ? AND user_id = ?", account.ID, account.MCPServerID, userID).
		Count(&count).Error
	return count == 1, err
}

// A GitHub App has one webhook URL. Production can optionally forward its
// verified deliveries to staging with a second shared secret, allowing both
// environments to use the same App without exposing an unsigned relay.
func forwardedWebhookSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validForwardedWebhook(body []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(forwardedWebhookSignature(body, secret)))
}

func forwardGitHubWebhook(body []byte, event, deliveryID string) error {
	url, secret := os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_URL"), os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET")
	if url == "" || secret == "" {
		return nil
	}
	return forwardGitHubWebhookTo(url, secret, body, event, deliveryID, &http.Client{Timeout: 10 * time.Second})
}

func forwardGitHubWebhookTo(url, secret string, body []byte, event, deliveryID string, client *http.Client) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	if deliveryID != "" {
		req.Header.Set("X-GitHub-Delivery", deliveryID)
	}
	req.Header.Set(forwardedWebhookSignatureHeader, forwardedWebhookSignature(body, secret))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("webhook relay returned %s", resp.Status)
	}
	return nil
}

func (api *API) GitHubStatus(w http.ResponseWriter, r *http.Request) {
	c, err := githubClient()
	if err != nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"configured": false, "error": err.Error()})
		return
	}
	var connections []db.GitHubConnection
	if err := api.db.Where("user_id = ?", api.currentUserID(r)).Order("id").Find(&connections).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not load GitHub connections")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"configured": true, "install_url": c.InstallURL(), "connections": connections})
}

// StartMCPGitHubOAuth starts GitHub App OAuth for a named MCP account. The
// resulting OAuth token is encrypted in that account; it is never shown in the
// UI or entered manually by the user.
func (api *API) StartMCPGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	server := api.mcpServerFromCtx(r)
	if server.Name != "github" {
		api.respondError(w, http.StatusBadRequest, "GitHub OAuth is only available for the GitHub integration")
		return
	}
	c, err := githubClient()
	if err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if deploymentURL() == "" {
		api.respondError(w, http.StatusInternalServerError, "DEPLOY_URL is required for GitHub OAuth")
		return
	}
	var input struct {
		Name       string `json:"name"`
		ReturnPath string `json:"return_path"`
		AccountID  int32  `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if input.Name == "" {
		input.Name = "GitHub account"
	}
	if input.ReturnPath == "" || !strings.HasPrefix(input.ReturnPath, "/") || strings.HasPrefix(input.ReturnPath, "//") {
		input.ReturnPath = "/settings"
	}
	if input.AccountID != 0 {
		var account db.MCPAccount
		if err := api.db.Where("id = ? AND mcp_server_id = ? AND user_id = ?", input.AccountID, server.ID, api.currentUserID(r)).First(&account).Error; err != nil {
			api.respondError(w, http.StatusNotFound, "GitHub account was not found")
			return
		}
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	callback := deploymentURL() + "/api/github/callback"
	if err := api.db.Create(&db.GitHubOAuthState{ID: state, RedirectURL: callback, MCPServerID: server.ID, MCPAccountID: input.AccountID, UserID: api.currentUserID(r), AccountName: input.Name, ReturnPath: input.ReturnPath, ExpiresAt: time.Now().Add(10 * time.Minute)}).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not start GitHub authorization")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"authorize_url": c.AuthorizeURL(state, callback, githubapp.AuthorizeOptions{SelectAccount: input.AccountID == 0}), "install_url": c.InstallURL()})
}
func (api *API) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	var s db.GitHubOAuthState
	if state == "" || api.db.First(&s, "id = ?", state).Error != nil || s.UsedAt != nil || time.Now().After(s.ExpiresAt) {
		http.Error(w, "GitHub authorization has expired. Return to Headcount1 and try again.", http.StatusBadRequest)
		return
	}
	// The callback is mounted behind RequireAuth. A state is not a bearer
	// credential: it is only valid in the same Headcount1 session user that
	// created it. Check this before spending the one-time state or exchanging
	// GitHub's code so a copied callback URL cannot attach credentials to a
	// different signed-in user.
	if !githubOAuthStateBelongsToUser(s, api.currentUserID(r)) {
		http.Error(w, "GitHub authorization belongs to a different Headcount1 user.", http.StatusForbidden)
		return
	}
	now := time.Now()
	// Claim state atomically. Two concurrent callbacks must not both exchange
	// the same GitHub authorization code and race to attach an account.
	claim := api.db.Model(&db.GitHubOAuthState{}).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", s.ID, now).
		Update("used_at", now)
	if claim.Error != nil {
		http.Error(w, "could not complete GitHub authorization", http.StatusInternalServerError)
		return
	}
	if claim.RowsAffected != 1 {
		http.Error(w, "GitHub authorization has expired or was already used. Return to Headcount1 and try again.", http.StatusBadRequest)
		return
	}
	c, err := githubClient()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	token, err := c.ExchangeCode(r.Context(), code, s.RedirectURL)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	identity, err := c.User(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	installs, err := c.UserInstallations(r.Context(), token)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if s.MCPServerID == 0 || s.UserID == 0 {
		http.Error(w, "GitHub authorization is not linked to an account", http.StatusBadRequest)
		return
	}
	sealed, sealErr := secrets.Default().EncryptForUser(s.UserID, token)
	if sealErr != nil {
		http.Error(w, "Your secure vault is locked. Return to Headcount1, unlock it, and try again.", http.StatusConflict)
		return
	}
	if _, err := api.persistGitHubOAuthAccount(r.Context(), s, identity, sealed, installs, now); err != nil {
		if errors.Is(err, errGitHubIdentityAlreadyConnected) {
			http.Redirect(w, r, deploymentURL()+s.ReturnPath+"?github=already_connected", http.StatusFound)
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "GitHub account was not found. Return to Headcount1 and try again.", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, deploymentURL()+s.ReturnPath+"?github=connected", http.StatusFound)
	return
}

// persistGitHubOAuthAccount atomically binds an OAuth identity to an account
// and refreshes its installations. GitHubIdentity has a unique composite
// index, so concurrent callbacks cannot create duplicate personal/work rows.
func (api *API) persistGitHubOAuthAccount(ctx context.Context, state db.GitHubOAuthState, identity githubapp.User, sealedToken string, installs []githubapp.Installation, now time.Time) (db.MCPAccount, error) {
	var saved db.MCPAccount
	err := api.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account db.MCPAccount
		if state.MCPAccountID != 0 {
			if err := tx.Where("id = ? AND mcp_server_id = ? AND user_id = ?", state.MCPAccountID, state.MCPServerID, state.UserID).First(&account).Error; err != nil {
				return err
			}
		}

		var existing db.GitHubIdentity
		err := tx.Where("mcp_server_id = ? AND user_id = ? AND git_hub_user_id = ?", state.MCPServerID, state.UserID, identity.ID).First(&existing).Error
		if err == nil && existing.MCPAccountID != state.MCPAccountID {
			return errGitHubIdentityAlreadyConnected
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if state.MCPAccountID == 0 {
			account = db.MCPAccount{MCPServerID: state.MCPServerID, Name: state.AccountName, AuthTokenEncrypted: sealedToken, UserID: &state.UserID}
			if err := tx.Create(&account).Error; err != nil {
				return err
			}
		} else {
			account.Name = state.AccountName
			account.AuthTokenEncrypted = sealedToken
			account.LastError = ""
			if err := tx.Save(&account).Error; err != nil {
				return err
			}
			if err := tx.Where("mcp_account_id = ?", account.ID).Delete(&db.GitHubConnection{}).Error; err != nil {
				return err
			}
		}
		var accountIdentity db.GitHubIdentity
		accountIdentityErr := tx.Where("mcp_account_id = ?", account.ID).First(&accountIdentity).Error
		switch {
		case errors.Is(accountIdentityErr, gorm.ErrRecordNotFound):
			if err := tx.Create(&db.GitHubIdentity{MCPAccountID: account.ID, MCPServerID: state.MCPServerID, UserID: state.UserID, GitHubUserID: identity.ID, GitHubLogin: identity.Login}).Error; err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					return errGitHubIdentityAlreadyConnected
				}
				return err
			}
		case accountIdentityErr != nil:
			return accountIdentityErr
		case accountIdentity.GitHubUserID != identity.ID || accountIdentity.GitHubLogin != identity.Login:
			accountIdentity.GitHubUserID = identity.ID
			accountIdentity.GitHubLogin = identity.Login
			accountIdentity.MCPServerID = state.MCPServerID
			accountIdentity.UserID = state.UserID
			if err := tx.Save(&accountIdentity).Error; err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					return errGitHubIdentityAlreadyConnected
				}
				return err
			}
		}
		for _, installation := range installs {
			connection := db.GitHubConnection{InstallationID: installation.ID, MCPAccountID: account.ID, UserID: state.UserID, AccountLogin: installation.Account.Login, ConnectedAt: now}
			if err := tx.Where("mcp_account_id = ? AND installation_id = ?", account.ID, installation.ID).Assign(connection).FirstOrCreate(&connection).Error; err != nil {
				return err
			}
		}
		saved = account
		return nil
	})
	return saved, err
}
func (api *API) ListGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	c, err := githubClient()
	if err != nil {
		api.respondError(w, 400, err.Error())
		return
	}
	userID := api.currentUserID(r)
	repos := []map[string]any{}
	accountStatuses := []map[string]any{}
	// Discover installations on every request rather than relying on the list
	// captured during OAuth. A user can change the GitHub App's repository
	// selection later, and the project picker must see that change immediately.
	var accounts []db.MCPAccount
	if err := api.db.Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").
		Where("mcp_accounts.user_id = ? AND mcp_servers.name = ?", userID, "github").Find(&accounts).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not load GitHub accounts")
		return
	}
	seen := map[int64]bool{}
	for _, account := range accounts {
		identityValid, identityErr := api.hasValidGitHubIdentity(r.Context(), account, userID)
		if identityErr != nil {
			api.respondError(w, http.StatusInternalServerError, "could not verify GitHub account identities")
			return
		}
		if !identityValid {
			message := "GitHub account identity must be verified. Reconnect this account."
			_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, message)
			accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name, "error": message})
			continue
		}
		token, decryptErr := secrets.Default().Decrypt(account.AuthTokenEncrypted)
		if decryptErr != nil || token == "" {
			message := "GitHub credentials are unavailable. Reconnect this account."
			_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, message)
			accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name, "error": message})
			continue
		}
		installs, installationsErr := c.UserInstallations(r.Context(), token)
		if installationsErr != nil {
			message := "GitHub authorization failed. Reconnect this account."
			_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, message)
			accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name, "error": message})
			continue
		}
		accountError := ""
		for _, installation := range installs {
			conn := db.GitHubConnection{InstallationID: installation.ID, MCPAccountID: account.ID, UserID: userID, AccountLogin: installation.Account.Login, ConnectedAt: time.Now()}
			if err := api.db.Where("mcp_account_id = ? AND installation_id = ?", account.ID, installation.ID).Assign(conn).FirstOrCreate(&conn).Error; err != nil {
				accountError = "Could not save GitHub installation metadata. Try again."
				break
			}
			rs, repositoriesErr := c.UserRepositories(r.Context(), token, installation.ID)
			if repositoriesErr != nil {
				accountError = "Could not load repositories for this GitHub account. Reconnect it and try again."
				break
			}
			for _, repo := range rs {
				if seen[repo.ID] {
					continue
				}
				seen[repo.ID] = true
				repos = append(repos, map[string]any{"id": repo.ID, "full_name": repo.FullName, "clone_url": repo.CloneURL, "html_url": repo.HTMLURL, "default_branch": repo.DefaultBranch, "installation_id": installation.ID, "account_id": account.ID})
			}
		}
		if accountError != "" {
			_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, accountError)
			accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name, "error": accountError})
			continue
		}
		_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, "")
		accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name})
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"repositories": repos, "accounts": accountStatuses})
}

type githubRepositorySelection struct {
	ID             int64 `json:"id"`
	InstallationID int64 `json:"installation_id"`
	AccountID      int32 `json:"account_id"`
}

// resolveGitHubRepository treats the browser payload as an opaque selection,
// then gets the canonical clone URL and branch from GitHub using a token owned
// by the current Headcount1 user. No client-supplied URL is ever persisted.
func (api *API) resolveGitHubRepository(ctx context.Context, userID int32, raw json.RawMessage) (githubRepositorySelection, githubapp.Repository, error) {
	var selection githubRepositorySelection
	if err := json.Unmarshal(raw, &selection); err != nil || selection.ID <= 0 || selection.InstallationID <= 0 || selection.AccountID <= 0 {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("invalid GitHub repository selection")
	}
	var account db.MCPAccount
	if err := api.db.Joins("JOIN mcp_servers ON mcp_servers.id = mcp_accounts.mcp_server_id").
		Where("mcp_accounts.id = ? AND mcp_accounts.user_id = ? AND mcp_servers.name = ?", selection.AccountID, userID, "github").
		First(&account).Error; err != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("GitHub account was not found")
	}
	identityValid, identityErr := api.hasValidGitHubIdentity(ctx, account, userID)
	if identityErr != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("could not verify GitHub account identity")
	}
	if !identityValid {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("GitHub account needs to be reconnected")
	}
	token, err := secrets.Default().Decrypt(account.AuthTokenEncrypted)
	if err != nil || token == "" {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("GitHub account needs to be reconnected")
	}
	c, err := githubClient()
	if err != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, err
	}
	repositories, err := c.UserRepositories(ctx, token, selection.InstallationID)
	if err != nil {
		_ = api.q.UpdateMCPAccountLastError(ctx, account.ID, "Could not load repositories for this GitHub account. Reconnect it and try again.")
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("could not verify the selected GitHub repository; refresh the list and try again")
	}
	for _, repository := range repositories {
		if repository.ID == selection.ID {
			_ = api.q.UpdateMCPAccountLastError(ctx, account.ID, "")
			return selection, repository, nil
		}
	}
	return githubRepositorySelection{}, githubapp.Repository{}, errors.New("selected GitHub repository is no longer permitted; refresh the list and try again")
}

func (api *API) applyGitHubRepositorySelection(ctx context.Context, userID int32, project *db.Project, raw json.RawMessage) error {
	selection, repository, err := api.resolveGitHubRepository(ctx, userID, raw)
	if err != nil {
		return err
	}
	project.GitHubRepositoryID = repository.ID
	project.GitHubInstallationID = selection.InstallationID
	project.RepositoryUrl = repository.CloneURL
	project.GitHubDefaultBranch = repository.DefaultBranch
	return nil
}

const (
	githubWebhookWakeTimeout = 10 * time.Minute
	// The lease must outlive a bounded wake attempt. Otherwise a redelivery
	// could reclaim the target while the first RerunTask is still running.
	githubWebhookLeaseDuration = 15 * time.Minute
)

func newGitHubWebhookAttemptToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook attempt token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// claimGitHubWebhookDelivery leases one delivery with a CAS token. A delivery
// may be reclaimed only once an earlier handler's lease has expired. Every
// later update is scoped to the token, so a stale request cannot overwrite a
// newer retry's result.
func (api *API) claimGitHubWebhookDelivery(ctx context.Context, deliveryID, event string) (db.GitHubWebhookDelivery, bool, error) {
	if deliveryID == "" {
		return db.GitHubWebhookDelivery{}, false, nil
	}
	var delivery db.GitHubWebhookDelivery
	err := api.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		delivery = db.GitHubWebhookDelivery{DeliveryID: deliveryID, Event: event, Status: "pending"}
		if createErr := api.db.WithContext(ctx).Create(&delivery).Error; createErr != nil {
			// Another server may have inserted the unique delivery ID between our
			// read and create. Loading it distinguishes that benign race from a
			// genuine persistence failure without relying on driver-specific errors.
			if loadErr := api.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; loadErr != nil {
				return db.GitHubWebhookDelivery{}, false, createErr
			}
		}
	} else if err != nil {
		return db.GitHubWebhookDelivery{}, false, err
	}
	if delivery.Status == "completed" {
		return delivery, true, nil
	}
	token, err := newGitHubWebhookAttemptToken()
	if err != nil {
		return db.GitHubWebhookDelivery{}, false, err
	}
	now := time.Now()
	leaseUntil := now.Add(githubWebhookLeaseDuration)
	claim := api.db.WithContext(ctx).Model(&db.GitHubWebhookDelivery{}).
		Where("delivery_id = ? AND status <> ? AND (status <> ? OR lease_expires_at IS NULL OR lease_expires_at <= ?)", deliveryID, "completed", "processing", now).
		Updates(map[string]any{"status": "processing", "attempt_token": token, "lease_expires_at": &leaseUntil, "attempts": gorm.Expr("attempts + 1"), "last_error": ""})
	if claim.Error != nil {
		return db.GitHubWebhookDelivery{}, false, claim.Error
	}
	if claim.RowsAffected == 0 {
		if err := api.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
			return db.GitHubWebhookDelivery{}, false, err
		}
		if delivery.Status == "completed" {
			return delivery, true, nil
		}
		return db.GitHubWebhookDelivery{}, false, fmt.Errorf("GitHub webhook delivery is already processing")
	}
	if err := api.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).First(&delivery).Error; err != nil {
		return db.GitHubWebhookDelivery{}, false, err
	}
	return delivery, false, nil
}

func (api *API) updateGitHubWebhookDelivery(ctx context.Context, deliveryID, attemptToken string, values map[string]any) error {
	if deliveryID == "" {
		return nil
	}
	result := api.db.WithContext(ctx).Model(&db.GitHubWebhookDelivery{}).
		Where("delivery_id = ? AND attempt_token = ?", deliveryID, attemptToken).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("GitHub webhook delivery lease was lost")
	}
	return nil
}

func (api *API) failGitHubWebhookDelivery(ctx context.Context, deliveryID, attemptToken string, cause error) {
	if err := api.updateGitHubWebhookDelivery(ctx, deliveryID, attemptToken, map[string]any{
		"status": "failed", "last_error": cause.Error(), "lease_expires_at": nil,
	}); err != nil {
		log.Printf("could not mark GitHub webhook delivery failed: %v", err)
	}
}

// wakeGitHubWebhookTargets is a small durable outbox worker. A comment is
// committed first, then its target row is leased and handed to the engine. If
// the process or engine call fails, the target remains pending and a GitHub
// redelivery retries only the wake — never the comment insert.
func (api *API) wakeGitHubWebhookTargets(ctx context.Context, deliveryID string) error {
	if deliveryID == "" || api.engine == nil {
		return nil
	}
	var targets []db.GitHubWebhookTarget
	if err := api.db.WithContext(ctx).Where("delivery_id = ? AND wake_status <> ?", deliveryID, "completed").Find(&targets).Error; err != nil {
		return fmt.Errorf("load GitHub webhook wake targets: %w", err)
	}
	for _, target := range targets {
		token, err := newGitHubWebhookAttemptToken()
		if err != nil {
			return err
		}
		now := time.Now()
		leaseUntil := now.Add(githubWebhookLeaseDuration)
		claim := api.db.WithContext(ctx).Model(&db.GitHubWebhookTarget{}).
			Where("id = ? AND wake_status <> ? AND (wake_status <> ? OR wake_lease_expires_at IS NULL OR wake_lease_expires_at <= ?)", target.ID, "completed", "processing", now).
			Updates(map[string]any{"wake_status": "processing", "wake_attempt_token": token, "wake_lease_expires_at": &leaseUntil, "wake_attempts": gorm.Expr("wake_attempts + 1"), "wake_last_error": ""})
		if claim.Error != nil {
			return fmt.Errorf("claim GitHub webhook wake target: %w", claim.Error)
		}
		if claim.RowsAffected == 0 {
			continue
		}
		wakeCtx, cancel := context.WithTimeout(context.Background(), githubWebhookWakeTimeout)
		err = api.engine.RerunTask(wakeCtx, target.TaskID)
		cancel()
		if err != nil {
			updateErr := api.db.WithContext(ctx).Model(&db.GitHubWebhookTarget{}).
				Where("id = ? AND wake_attempt_token = ?", target.ID, token).
				Updates(map[string]any{"wake_status": "pending", "wake_lease_expires_at": nil, "wake_last_error": err.Error()}).Error
			if updateErr != nil {
				return fmt.Errorf("record GitHub webhook wake failure: %w", updateErr)
			}
			return fmt.Errorf("rerun task %d for GitHub webhook: %w", target.TaskID, err)
		}
		result := api.db.WithContext(ctx).Model(&db.GitHubWebhookTarget{}).
			Where("id = ? AND wake_attempt_token = ?", target.ID, token).
			Updates(map[string]any{"wake_status": "completed", "wake_lease_expires_at": nil, "wake_last_error": ""})
		if result.Error != nil {
			return fmt.Errorf("complete GitHub webhook wake target: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("GitHub webhook wake target lease was lost")
		}
	}
	var incomplete int64
	if err := api.db.WithContext(ctx).Model(&db.GitHubWebhookTarget{}).Where("delivery_id = ? AND wake_status <> ?", deliveryID, "completed").Count(&incomplete).Error; err != nil {
		return fmt.Errorf("count incomplete GitHub webhook wakes: %w", err)
	}
	if incomplete != 0 {
		return fmt.Errorf("GitHub webhook wake is still being processed")
	}
	return nil
}

func (api *API) GitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		api.respondError(w, 400, "invalid webhook body")
		return
	}
	forwarded := r.Header.Get(forwardedWebhookSignatureHeader) != ""
	if forwarded {
		if !validForwardedWebhook(body, r.Header.Get(forwardedWebhookSignatureHeader), os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET")) {
			api.respondError(w, 401, "invalid forwarded webhook signature")
			return
		}
	} else {
		c, err := githubClient()
		if err != nil || !c.VerifyWebhook(body, r.Header.Get("X-Hub-Signature-256")) {
			api.respondError(w, 401, "invalid GitHub webhook signature")
			return
		}
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		api.respondError(w, http.StatusBadRequest, "X-GitHub-Delivery is required")
		return
	}
	// Webhook work must not inherit the HTTP request cancellation. This bounded
	// context covers delivery claim, durable comment/outbox writes, agent wake,
	// and the final lease-guarded completion transition.
	webhookCtx, cancel := context.WithTimeout(context.Background(), githubWebhookLeaseDuration)
	defer cancel()
	event := r.Header.Get("X-GitHub-Event")
	var p githubWebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid GitHub event")
		return
	}
	delivery, completed, err := api.claimGitHubWebhookDelivery(webhookCtx, deliveryID, event)
	if err != nil {
		if strings.Contains(err.Error(), "already processing") {
			api.respondError(w, http.StatusConflict, err.Error())
		} else {
			api.respondError(w, http.StatusInternalServerError, "could not claim GitHub webhook delivery")
		}
		return
	}
	if completed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	markFailed := func(cause error) {
		api.failGitHubWebhookDelivery(webhookCtx, deliveryID, delivery.AttemptToken, cause)
	}
	// Relay before acknowledging the original delivery. A successful relay is
	// persisted, so a later local failure retries only local work; a failed
	// relay remains retryable rather than silently losing staging events.
	if !forwarded && deliveryID != "" && delivery.ForwardedAt == nil {
		if err := forwardGitHubWebhook(body, event, deliveryID); err != nil {
			markFailed(fmt.Errorf("forward GitHub webhook: %w", err))
			api.respondError(w, http.StatusBadGateway, "could not forward GitHub webhook")
			return
		}
		now := time.Now()
		if err := api.updateGitHubWebhookDelivery(webhookCtx, deliveryID, delivery.AttemptToken, map[string]any{"forwarded_at": &now}); err != nil {
			markFailed(fmt.Errorf("record forwarded GitHub webhook: %w", err))
			api.respondError(w, http.StatusInternalServerError, "could not record GitHub webhook relay")
			return
		}
	}
	pr := p.PullRequest.Number
	if pr == 0 {
		pr = p.Issue.Number
	}
	if pr == 0 && len(p.WorkflowRun.PullRequests) > 0 {
		pr = p.WorkflowRun.PullRequests[0].Number
	}
	complete := func() bool {
		now := time.Now()
		if err := api.updateGitHubWebhookDelivery(webhookCtx, deliveryID, delivery.AttemptToken, map[string]any{"status": "completed", "completed_at": &now, "last_error": "", "lease_expires_at": nil}); err != nil {
			api.respondError(w, http.StatusInternalServerError, "could not complete GitHub webhook delivery")
			return false
		}
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	if pr == 0 {
		complete()
		return
	}
	content := ""
	switch event {
	case "issue_comment":
		// GitHub sends the same event for edits and deletions. Only newly
		// created comments represent new work for an agent.
		if p.Action == "created" && p.Issue.PullRequest != nil {
			content = fmt.Sprintf("GitHub comment from @%s on PR #%d: %s", p.Comment.User.Login, pr, p.Comment.Body)
		}
	case "workflow_run":
		if p.WorkflowRun.Conclusion == "failure" {
			content = fmt.Sprintf("GitHub Actions failed for PR #%d: %s", pr, p.WorkflowRun.HTMLURL)
		}
	}
	if content == "" {
		complete()
		return
	}
	comments := []db.Comment{}
	err = api.db.WithContext(webhookCtx).Transaction(func(tx *gorm.DB) error {
		var projects []db.Project
		if err := tx.Where("git_hub_repository_id = ?", p.Repository.ID).Find(&projects).Error; err != nil {
			return fmt.Errorf("load projects for GitHub webhook: %w", err)
		}
		for _, project := range projects {
			var task db.Task
			err := tx.Where("project_id = ? AND git_hub_pr_number = ?", project.ID, pr).Order("id desc").First(&task).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("load task for GitHub webhook: %w", err)
			}
			if deliveryID != "" {
				var target db.GitHubWebhookTarget
				err := tx.Where("delivery_id = ? AND task_id = ?", deliveryID, task.ID).First(&target).Error
				if err == nil {
					continue
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("load GitHub webhook target: %w", err)
				}
			}
			comment := db.Comment{TaskID: task.ID, AuthorType: "github", CommentType: "github", Content: content}
			if err := tx.Create(&comment).Error; err != nil {
				return fmt.Errorf("save GitHub comment: %w", err)
			}
			if deliveryID != "" {
				if err := tx.Create(&db.GitHubWebhookTarget{DeliveryID: deliveryID, TaskID: task.ID, CommentID: comment.ID}).Error; err != nil {
					return fmt.Errorf("save GitHub webhook target: %w", err)
				}
			}
			comments = append(comments, comment)
		}
		return nil
	})
	if err != nil {
		markFailed(err)
		api.respondError(w, http.StatusInternalServerError, "could not process GitHub webhook")
		return
	}
	for _, comment := range comments {
		if api.hub != nil {
			api.hub.BroadcastEvent("comment_created", comment)
		}
	}
	if err := api.wakeGitHubWebhookTargets(webhookCtx, deliveryID); err != nil {
		markFailed(err)
		api.respondError(w, http.StatusInternalServerError, "could not wake task for GitHub webhook")
		return
	}
	complete()
}

type githubWebhookPayload struct {
	Action     string `json:"action"`
	Repository struct {
		ID int64 `json:"id"`
	} `json:"repository"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Issue struct {
		Number      int `json:"number"`
		PullRequest any `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	WorkflowRun struct {
		Conclusion   string `json:"conclusion"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
		HTMLURL string `json:"html_url"`
	} `json:"workflow_run"`
}

func GitHubTokenForProject(ctx context.Context, project db.Project) (string, error) {
	return githubapp.TokenForProject(ctx, project)
}
