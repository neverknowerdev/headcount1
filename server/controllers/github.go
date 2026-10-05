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

func githubOAuthStateBelongsToUser(state db.GitHubOAuthState, userID int32) bool {
	return userID != 0 && state.UserID != 0 && state.UserID == userID
}

// hasValidGitHubIdentity ensures the generic MCP account is still bound to the
// durable OAuth identity that was verified at callback time. In particular,
// this keeps disabled legacy/duplicate rows from being resurrected merely
// because they still contain an encrypted token.
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
	if api.currentUserID(r) == 0 {
		api.respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	c, err := githubClient()
	if err != nil {
		api.respondJSON(w, http.StatusOK, map[string]any{"configured": false, "error": err.Error()})
		return
	}
	connections, err := api.q.ListGitHubConnectionsForUser(r.Context(), api.currentUserID(r))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not load GitHub connections")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]any{"configured": true, "install_url": c.InstallURL(), "connections": connections})
}

// StartMCPGitHubOAuth starts GitHub App OAuth. The resulting OAuth token and
// GitHub login are discovered during the callback; neither is entered manually.
func (api *API) StartMCPGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	server := api.mcpServerFromCtx(r)
	userID := api.currentUserID(r)
	if userID == 0 {
		api.respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !server.IsGitHub() {
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
		ReturnPath string `json:"return_path"`
		AccountID  int32  `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if input.ReturnPath == "" || !strings.HasPrefix(input.ReturnPath, "/") || strings.HasPrefix(input.ReturnPath, "//") {
		input.ReturnPath = "/settings"
	}
	if input.AccountID != 0 {
		if _, err := api.q.GetGitHubAccountForUser(r.Context(), input.AccountID, server.ID, userID); err != nil {
			api.respondError(w, http.StatusNotFound, "GitHub account was not found")
			return
		}
	}
	selectAccount, err := api.shouldSelectGitHubAccount(r.Context(), server.ID, userID, input.AccountID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not start GitHub authorization")
		return
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	callback := deploymentURL() + "/api/github/callback"
	if err := api.q.CreateGitHubOAuthState(r.Context(), db.GitHubOAuthState{ID: state, RedirectURL: callback, MCPServerID: server.ID, MCPAccountID: input.AccountID, UserID: userID, ReturnPath: input.ReturnPath, ExpiresAt: time.Now().Add(10 * time.Minute)}); err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not start GitHub authorization")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"authorize_url": c.AuthorizeURL(state, callback, githubapp.AuthorizeOptions{SelectAccount: selectAccount}), "install_url": c.InstallURL()})
}

// shouldSelectGitHubAccount requests GitHub's identity chooser only when a
// user is adding another GitHub account. The first connection uses GitHub's
// normal OAuth page, while re-authentication stays bound to its account.
func (api *API) shouldSelectGitHubAccount(ctx context.Context, serverID, userID, accountID int32) (bool, error) {
	if accountID != 0 {
		return false, nil
	}
	count, err := api.q.CountGitHubAccountsForUser(ctx, serverID, userID)
	return count > 0, err
}

func (api *API) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	userID := api.currentUserID(r)
	if userID == 0 {
		api.respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "GitHub authorization code is missing. Return to Headcount1 and try again.", http.StatusBadRequest)
		return
	}
	s, stateErr := api.q.GetGitHubOAuthState(r.Context(), state)
	if state == "" || stateErr != nil || s.UsedAt != nil || time.Now().After(s.ExpiresAt) {
		http.Error(w, "GitHub authorization has expired. Return to Headcount1 and try again.", http.StatusBadRequest)
		return
	}
	// The callback is mounted behind RequireAuth. A state is not a bearer
	// credential: it is only valid in the same Headcount1 session user that
	// created it. Check this before spending the one-time state or exchanging
	// GitHub's code so a copied callback URL cannot attach credentials to a
	// different signed-in user.
	if !githubOAuthStateBelongsToUser(s, userID) {
		http.Error(w, "GitHub authorization belongs to a different Headcount1 user.", http.StatusForbidden)
		return
	}
	now := time.Now()
	if _, serverErr := api.q.GetBuiltinGitHubServer(r.Context(), s.MCPServerID); serverErr != nil {
		http.Error(w, "GitHub authorization is not linked to the built-in GitHub integration.", http.StatusBadRequest)
		return
	}
	// Claim state atomically. Two concurrent callbacks must not both exchange
	// the same GitHub authorization code and race to attach an account.
	claimed, claimErr := api.q.ClaimGitHubOAuthState(r.Context(), s.ID, now)
	if claimErr != nil {
		http.Error(w, "could not complete GitHub authorization", http.StatusInternalServerError)
		return
	}
	if !claimed {
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
		if errors.Is(err, db.ErrGitHubIdentityAlreadyConnected) {
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
	records := make([]db.GitHubInstallationRecord, 0, len(installs))
	for _, installation := range installs {
		records = append(records, db.GitHubInstallationRecord{InstallationID: installation.ID, AccountLogin: installation.Account.Login})
	}
	return api.q.SaveGitHubOAuthAccount(ctx, db.SaveGitHubOAuthAccountParams{
		State: state, GitHubUserID: identity.ID, GitHubLogin: identity.Login,
		SealedToken: sealedToken, Installations: records, ConnectedAt: now,
	})
}
func (api *API) ListGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	if api.currentUserID(r) == 0 {
		api.respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}
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
	accounts, err := api.q.ListGitHubAccountsForUser(r.Context(), userID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "could not load GitHub accounts")
		return
	}
	seen := map[int64]bool{}
	for _, account := range accounts {
		identityValid, identityErr := api.q.HasGitHubIdentity(r.Context(), account, userID)
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
		if account.UserID == nil {
			message := "GitHub account is missing its owner. Reconnect this account."
			_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, message)
			accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name, "error": message})
			continue
		}
		connections, connectionsErr := api.q.ListGitHubConnectionsForAccount(r.Context(), account.ID, *account.UserID)
		if connectionsErr != nil || len(connections) == 0 {
			message := "No GitHub App installation is linked to this account. Reconnect this account."
			_ = api.q.UpdateMCPAccountLastError(r.Context(), account.ID, message)
			accountStatuses = append(accountStatuses, map[string]any{"id": account.ID, "name": account.Name, "error": message})
			continue
		}
		accountError := ""
		for _, connection := range connections {
			token, tokenErr := c.InstallationToken(r.Context(), connection.InstallationID, 0)
			if tokenErr != nil {
				accountError = "Could not refresh GitHub App access. Check the deployment GitHub App configuration."
				break
			}
			rs, repositoriesErr := c.InstallationRepositories(r.Context(), token)
			if repositoriesErr != nil {
				accountError = "Could not load repositories from this GitHub App installation. Check its repository permissions and try again."
				break
			}
			for _, repo := range rs {
				if seen[repo.ID] {
					continue
				}
				seen[repo.ID] = true
				repos = append(repos, map[string]any{"id": repo.ID, "full_name": repo.FullName, "clone_url": repo.CloneURL, "html_url": repo.HTMLURL, "default_branch": repo.DefaultBranch, "installation_id": connection.InstallationID, "account_id": account.ID})
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
	account, err := api.q.GetGitHubAccountByIDForUser(ctx, selection.AccountID, userID)
	if err != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("GitHub account was not found")
	}
	identityValid, identityErr := api.q.HasGitHubIdentity(ctx, account, userID)
	if identityErr != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("could not verify GitHub account identity")
	}
	if !identityValid {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("GitHub account needs to be reconnected")
	}
	if account.UserID == nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("GitHub account needs to be reconnected")
	}
	if _, err := api.q.GetGitHubConnectionForAccountInstallation(ctx, account.ID, *account.UserID, selection.InstallationID); err != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("selected GitHub installation is no longer connected to this account")
	}
	c, err := githubClient()
	if err != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, err
	}
	token, err := c.InstallationToken(ctx, selection.InstallationID, 0)
	if err != nil {
		return githubRepositorySelection{}, githubapp.Repository{}, errors.New("could not refresh GitHub App access; check deployment configuration")
	}
	repositories, err := c.InstallationRepositories(ctx, token)
	if err != nil {
		_ = api.q.UpdateMCPAccountLastError(ctx, account.ID, "Could not load repositories from this GitHub App installation. Check its repository permissions and try again.")
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
	token, err := newGitHubWebhookAttemptToken()
	if err != nil {
		return db.GitHubWebhookDelivery{}, false, err
	}
	now := time.Now()
	leaseUntil := now.Add(githubWebhookLeaseDuration)
	return api.q.ClaimGitHubWebhookDelivery(ctx, deliveryID, event, token, now, leaseUntil)
}

func (api *API) updateGitHubWebhookDelivery(ctx context.Context, deliveryID, attemptToken string, values map[string]any) error {
	if deliveryID == "" {
		return nil
	}
	return api.q.UpdateGitHubWebhookDelivery(ctx, deliveryID, attemptToken, values)
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
	targets, err := api.q.ListPendingGitHubWebhookTargets(ctx, deliveryID)
	if err != nil {
		return fmt.Errorf("load GitHub webhook wake targets: %w", err)
	}
	for _, target := range targets {
		token, err := newGitHubWebhookAttemptToken()
		if err != nil {
			return err
		}
		now := time.Now()
		leaseUntil := now.Add(githubWebhookLeaseDuration)
		claimed, claimErr := api.q.ClaimGitHubWebhookTarget(ctx, target.ID, token, now, leaseUntil)
		if claimErr != nil {
			return fmt.Errorf("claim GitHub webhook wake target: %w", claimErr)
		}
		if !claimed {
			continue
		}
		wakeCtx, cancel := context.WithTimeout(context.Background(), githubWebhookWakeTimeout)
		err = api.engine.RerunTask(wakeCtx, target.TaskID)
		cancel()
		if err != nil {
			updateErr := api.q.UpdateGitHubWebhookTarget(ctx, target.ID, token, map[string]any{"wake_status": "pending", "wake_lease_expires_at": nil, "wake_last_error": err.Error()})
			if updateErr != nil {
				return fmt.Errorf("record GitHub webhook wake failure: %w", updateErr)
			}
			return fmt.Errorf("rerun task %d for GitHub webhook: %w", target.TaskID, err)
		}
		if err := api.q.UpdateGitHubWebhookTarget(ctx, target.ID, token, map[string]any{"wake_status": "completed", "wake_lease_expires_at": nil, "wake_last_error": ""}); err != nil {
			return fmt.Errorf("complete GitHub webhook wake target: %w", err)
		}
	}
	incomplete, err := api.q.CountPendingGitHubWebhookTargets(ctx, deliveryID)
	if err != nil {
		return fmt.Errorf("count incomplete GitHub webhook wakes: %w", err)
	}
	if incomplete != 0 {
		return fmt.Errorf("GitHub webhook wake is still being processed")
	}
	return nil
}

func (api *API) GitHubWebhook(w http.ResponseWriter, r *http.Request) {
	request, requestErr := api.parseGitHubWebhookRequest(w, r)
	if requestErr != nil {
		api.respondError(w, requestErr.status, requestErr.message)
		return
	}
	webhookCtx, cancel := context.WithTimeout(context.Background(), githubWebhookLeaseDuration)
	defer cancel()
	if processErr := api.processGitHubWebhook(webhookCtx, request); processErr != nil {
		api.respondError(w, processErr.status, processErr.message)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type githubWebhookRequest struct {
	body       []byte
	event      string
	deliveryID string
	forwarded  bool
	payload    githubWebhookPayload
}

type githubWebhookError struct {
	status  int
	message string
}

func (api *API) parseGitHubWebhookRequest(w http.ResponseWriter, r *http.Request) (githubWebhookRequest, *githubWebhookError) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return githubWebhookRequest{}, &githubWebhookError{http.StatusBadRequest, "invalid webhook body"}
	}
	forwarded := r.Header.Get(forwardedWebhookSignatureHeader) != ""
	if forwarded && !validForwardedWebhook(body, r.Header.Get(forwardedWebhookSignatureHeader), os.Getenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET")) {
		return githubWebhookRequest{}, &githubWebhookError{http.StatusUnauthorized, "invalid forwarded webhook signature"}
	}
	if !forwarded {
		c, err := githubClient()
		if err != nil || !c.VerifyWebhook(body, r.Header.Get("X-Hub-Signature-256")) {
			return githubWebhookRequest{}, &githubWebhookError{http.StatusUnauthorized, "invalid GitHub webhook signature"}
		}
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if deliveryID == "" {
		return githubWebhookRequest{}, &githubWebhookError{http.StatusBadRequest, "X-GitHub-Delivery is required"}
	}
	event := r.Header.Get("X-GitHub-Event")
	var payload githubWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return githubWebhookRequest{}, &githubWebhookError{http.StatusBadRequest, "invalid GitHub event"}
	}
	return githubWebhookRequest{body: body, event: event, deliveryID: deliveryID, forwarded: forwarded, payload: payload}, nil
}

func (api *API) processGitHubWebhook(ctx context.Context, request githubWebhookRequest) *githubWebhookError {
	delivery, completed, err := api.claimGitHubWebhookDelivery(ctx, request.deliveryID, request.event)
	if err != nil {
		if errors.Is(err, db.ErrGitHubWebhookAlreadyProcessing) {
			return &githubWebhookError{http.StatusConflict, err.Error()}
		}
		return &githubWebhookError{http.StatusInternalServerError, "could not claim GitHub webhook delivery"}
	}
	if completed {
		return nil
	}
	markFailed := func(cause error) {
		api.failGitHubWebhookDelivery(ctx, request.deliveryID, delivery.AttemptToken, cause)
	}
	if relayErr := api.relayGitHubWebhook(ctx, request, delivery); relayErr != nil {
		markFailed(relayErr)
		return &githubWebhookError{http.StatusBadGateway, "could not forward GitHub webhook"}
	}
	pr, content := githubWebhookTaskComment(request.event, request.payload)
	if content == "" {
		return api.completeGitHubWebhook(ctx, request.deliveryID, delivery.AttemptToken)
	}
	comments, err := api.q.CreateGitHubWebhookComments(ctx, request.deliveryID, request.payload.Repository.ID, pr, content)
	if err != nil {
		markFailed(err)
		return &githubWebhookError{http.StatusInternalServerError, "could not process GitHub webhook"}
	}
	for _, comment := range comments {
		if api.hub != nil {
			api.hub.BroadcastEvent("comment_created", comment)
		}
	}
	if err := api.wakeGitHubWebhookTargets(ctx, request.deliveryID); err != nil {
		markFailed(err)
		return &githubWebhookError{http.StatusInternalServerError, "could not wake task for GitHub webhook"}
	}
	return api.completeGitHubWebhook(ctx, request.deliveryID, delivery.AttemptToken)
}

func (api *API) relayGitHubWebhook(ctx context.Context, request githubWebhookRequest, delivery db.GitHubWebhookDelivery) error {
	if request.forwarded || delivery.ForwardedAt != nil {
		return nil
	}
	if err := forwardGitHubWebhook(request.body, request.event, request.deliveryID); err != nil {
		return fmt.Errorf("forward GitHub webhook: %w", err)
	}
	now := time.Now()
	if err := api.updateGitHubWebhookDelivery(ctx, request.deliveryID, delivery.AttemptToken, map[string]any{"forwarded_at": &now}); err != nil {
		return fmt.Errorf("record forwarded GitHub webhook: %w", err)
	}
	return nil
}

func (api *API) completeGitHubWebhook(ctx context.Context, deliveryID, attemptToken string) *githubWebhookError {
	now := time.Now()
	if err := api.updateGitHubWebhookDelivery(ctx, deliveryID, attemptToken, map[string]any{"status": "completed", "completed_at": &now, "last_error": "", "lease_expires_at": nil}); err != nil {
		return &githubWebhookError{http.StatusInternalServerError, "could not complete GitHub webhook delivery"}
	}
	return nil
}

func githubWebhookTaskComment(event string, payload githubWebhookPayload) (int, string) {
	pr := payload.PullRequest.Number
	if pr == 0 {
		pr = payload.Issue.Number
	}
	if pr == 0 && len(payload.WorkflowRun.PullRequests) > 0 {
		pr = payload.WorkflowRun.PullRequests[0].Number
	}
	if pr == 0 {
		return 0, ""
	}
	switch event {
	case "issue_comment":
		if payload.Action == "created" && payload.Issue.PullRequest != nil {
			return pr, fmt.Sprintf("GitHub comment from @%s on PR #%d: %s", payload.Comment.User.Login, pr, payload.Comment.Body)
		}
	case "workflow_run":
		if payload.WorkflowRun.Conclusion == "failure" {
			return pr, fmt.Sprintf("GitHub Actions failed for PR #%d: %s", pr, payload.WorkflowRun.HTMLURL)
		}
	}
	return pr, ""
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
