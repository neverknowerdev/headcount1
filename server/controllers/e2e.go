package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"
	"agent-orchestrator/pkg/secrets"
	"agent-orchestrator/pkg/utils"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

var e2eResetMu sync.Mutex

// WipeDB clears all data from the database. Only available when E2E_MODE=true.
// Route registration is guarded by the env var in main.go.
func (api *API) WipeDB(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "WipeDB is only available in E2E mode", http.StatusForbidden)
		return
	}

	// A reset must never race an agent goroutine that is still writing to the
	// database. The browser suite deliberately reuses one server process, so a
	// previous test that timed out can otherwise repopulate rows after this
	// handler deletes them. Serialize resets and stop active runs first.
	e2eResetMu.Lock()
	defer e2eResetMu.Unlock()
	// PostgreSQL can take longer to flush the final run-log/bookkeeping writes
	// after a large orchestration scenario; keep the wait bounded but allow that
	// legitimate drain to finish before deleting shared E2E state.
	resetCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := api.stopE2ERuns(resetCtx); err != nil {
		api.respondError(w, http.StatusConflict, "cannot reset E2E database while runs are active: "+err.Error())
		return
	}

	// Order matters: foreign keys are enforced, so every child is deleted
	// before its parent. The identity graph (sessions, keys, memberships,
	// teams, users) is wiped LAST — companies, providers, model groups and
	// default-model settings all reference users/teams and must go first.
	tables := []string{
		"activity_logs",
		"proxy_request_logs",
		"model_request_stats",
		"model_group_members",
		"model_groups",
		"default_model_settings",
		"run_events",
		"run_status_reports",
		"runs",
		"comments",
		"attachments",
		"tasks",
		"skills",
		"agent_mcp_accounts",
		"agents",
		"llm_providers",
		"sprints",
		"projects",
		"companies",
		// MCP tables — clear dependents before mcp_servers (FK order)
		"agent_mcp_tool_filters",
		"mcp_tool_stats",
		"agent_mcp_servers",
		"mcp_accounts",
		"mcp_servers",
		// Identity graph last (everything above may reference users/teams).
		"team_invites",
		"password_reset_tokens",
		"sessions",
		"refresh_tokens",
		"user_git_credentials",
		"web_authn_sessions",
		"web_authn_credentials",
		"team_members",
		"teams",
		"users",
	}
	if err := api.db.Transaction(func(tx *gorm.DB) error {
		for _, table := range tables {
			if err := tx.Exec("DELETE FROM " + table).Error; err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		// Reset autoincrement so test IDs start at 1. SQLite keeps its counters
		// in sqlite_sequence; Postgres realigns each table's serial sequence.
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Exec("DELETE FROM sqlite_sequence").Error; err != nil {
				return fmt.Errorf("reset SQLite sequences: %w", err)
			}
			return nil
		}
		for _, table := range tables {
			// Skip tables without an id column (composite-key join tables).
			var hasID bool
			if err := tx.Raw(
				`SELECT EXISTS (SELECT 1 FROM information_schema.columns
				 WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'id')`,
				table,
			).Scan(&hasID).Error; err != nil {
				return fmt.Errorf("inspect %s id column: %w", table, err)
			} else if !hasID {
				continue
			}
			var seq *string
			if err := tx.Raw(`SELECT pg_get_serial_sequence(?, 'id')`, table).Scan(&seq).Error; err != nil {
				return fmt.Errorf("find %s sequence: %w", table, err)
			} else if seq == nil {
				continue
			}
			if err := tx.Exec(`SELECT setval(?, 1, false)`, *seq).Error; err != nil {
				return fmt.Errorf("reset %s sequence: %w", table, err)
			}
		}
		return nil
	}); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to reset E2E database: "+err.Error())
		return
	}

	// Re-seed built-in MCP servers so tests that list servers get a consistent baseline.
	if err := db.New(api.db).EnsureBuiltinMCPServers(resetCtx); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to recreate builtin MCP servers: "+err.Error())
		return
	}

	// Recreate the e2e fixture user (auth is always on; the wipe removed it).
	// Its onUserCreated hook seeds the per-user builtin providers and default
	// model settings.
	if _, err := api.e2eUser(resetCtx); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to recreate e2e user: "+err.Error())
		return
	}

	// Re-seed built-in LLM providers (OpenRouter, OpenCode Zen) so tests that
	// list providers get a consistent baseline. A wipe intentionally never
	// makes a live network call (stays fast and deterministic), so give them
	// an immediate placeholder catalog instead: without it, DefaultModel
	// would stay blank until the real, once-per-process background
	// discovery fetch completes — which, after the first wipe, may never
	// happen again — silently blocking anything that assumes a builtin
	// provider has a usable default (e.g. the "existing provider" onboarding
	// step's required Model Name field).
	q := db.New(api.db)
	seedPlaceholderModelCatalog(resetCtx, q)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// stopE2ERuns cancels every process-local running session and waits for both
// the database terminal transition and the engine goroutines to drain. The
// latter matters because a session can finish its control-plane update before
// its final bookkeeping writes have returned. It is intentionally bounded so
// a broken engine cannot turn a test reset into a CI-job-sized hang.
func (api *API) stopE2ERuns(ctx context.Context) error {
	activeStatuses := []string{"running", "resuming", "waiting"}
	type runQuiescer interface {
		WaitForActiveRuns(context.Context)
	}
	quiescer, canWaitForGoroutines := api.engine.(runQuiescer)
	for {
		var runs []db.Run
		if err := api.db.WithContext(ctx).Where("status IN ?", activeStatuses).Find(&runs).Error; err != nil {
			return fmt.Errorf("list active runs: %w", err)
		}
		for _, run := range runs {
			api.engine.StopRun(ctx, run.ID)
		}
		if canWaitForGoroutines {
			quiescer.WaitForActiveRuns(ctx)
		}
		var active int64
		if err := api.db.WithContext(ctx).Model(&db.Run{}).Where("status IN ?", activeStatuses).Count(&active).Error; err != nil {
			return fmt.Errorf("check active runs: %w", err)
		}
		if active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// E2ERegister creates an account without a WebAuthn ceremony, for E2E tests
// that need a second real user (e.g. invite-join flows). It mirrors the passkey
// RegisterFinish account setup — create user, accept invite or make a team,
// seed builtins, unlock deterministically — and issues a session cookie.
func (api *API) E2ERegister(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "not available", http.StatusForbidden)
		return
	}
	var req struct {
		Email       string `json:"email"`
		InviteToken string `json:"invite_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	email := db.NormalizeEmail(req.Email)
	if _, err := api.q.GetUserByEmail(r.Context(), email); err == nil {
		api.respondError(w, http.StatusConflict, "already exists")
		return
	}
	if req.InviteToken != "" {
		if _, err := api.q.GetTeamInviteByTokenHash(r.Context(), authctx.HashToken(req.InviteToken)); err != nil {
			api.respondError(w, http.StatusBadRequest, "invalid or expired invite")
			return
		}
	}
	user, err := api.q.CreateUser(r.Context(), email)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "create failed")
		return
	}
	// Email-bound invite acceptance (same rule as RegisterFinish); fall back to
	// a personal team on mismatch/no-invite.
	if !api.acceptTeamInviteFor(r.Context(), req.InviteToken, user) {
		_ = api.q.EnsureTeamForUser(r.Context(), user)
	}
	secrets.UnlockUser(user.ID, e2eDEK(), keyringTTL())
	api.seedNewUser(r.Context(), user.ID)
	// A distinct session cookie for this user (the fixture bypass only applies
	// to requests without a cookie).
	if err := api.issueTokenPair(w, r, user); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusCreated, map[string]any{"user": userResponse{ID: user.ID, Email: user.Email, IsAdmin: user.IsAdmin}})
}

// E2ELock evicts the fixture user's DEK so tests can exercise the locked
// vault state (crash re-tap). E2E-only.
func (api *API) E2ELock(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "not available", http.StatusForbidden)
		return
	}
	if u, err := api.q.GetUserByEmail(r.Context(), e2eUserEmail); err == nil {
		secrets.LockUser(u.ID)
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "locked"})
}

// E2ERevealProviderSecret returns the DECRYPTED api_key of an LLM provider.
// E2E-only: the real API never exposes raw secrets (only has_api_key). This
// exists so the backup/restore e2e can prove a secret round-trips through the
// zero-knowledge pipeline — sealed as enc:u1 under the (deterministic, in E2E)
// user DEK, carried through backup + restore, then decrypted again after the
// fixture user is re-unlocked. A successful reveal is end-to-end proof the
// ciphertext survived and is still openable with the restored keyring.
func (api *API) E2ERevealProviderSecret(w http.ResponseWriter, r *http.Request) {
	if !utils.IsE2E() {
		http.Error(w, "not available", http.StatusForbidden)
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "bad id")
		return
	}
	// Ensure the fixture user is unlocked (deterministic DEK) so the serializer
	// can decrypt on read.
	if _, err := api.e2eUser(r.Context()); err != nil {
		api.respondError(w, http.StatusInternalServerError, "unlock failed")
		return
	}
	p, err := api.q.GetLLMProvider(r.Context(), int32(id))
	if err != nil {
		api.respondError(w, http.StatusNotFound, "provider not found")
		return
	}
	apiKey, err := secrets.Default().Decrypt(p.ApiKeyEncrypted)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "decrypt failed")
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"api_key": apiKey})
}

// seedPlaceholderModelCatalog gives freshly re-seeded builtin providers a
// non-empty model catalog immediately, without any network call, so e2e
// tests that list/use providers right after a wipe never see a blank
// DefaultModel/SupportedModels. It's deliberately not a real model — a
// builtin provider's actual catalog only ever comes from a live
// pkg/llmdiscovery fetch (see main.go); production never calls this.
func seedPlaceholderModelCatalog(ctx context.Context, q *db.Queries) {
	providers, err := q.ListLLMProviders(ctx)
	if err != nil {
		return
	}
	for _, p := range providers {
		if !p.Builtin {
			continue
		}
		_ = q.UpdateLLMProviderModelCatalog(ctx, p.ID, []string{"e2e-placeholder-model"})
	}
}
