package endpoints

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/envstore"
	"agent-orchestrator/pkg/updater"
	"agent-orchestrator/pkg/utils"
)

// DeployEventType classifies where a deploy event originated. CI sets it; the
// server uses it (together with its environment and the deploy_source setting)
// to decide whether to act.
const (
	deployEventRelease = "release" // a published GitHub release
	deployEventMain    = "main"    // a push to the main branch
	deployEventBranch  = "branch"  // a push to a feature/PR branch
)

// DeployWebhookPayload is what CI POSTs to /api/deploy/webhook. download_url is
// the URL of the built binary asset for this build, and sha256 pins its exact
// contents — the server rejects a URL outside its allowed deploy sources and a
// binary whose digest doesn't match (see updater.Deploy), so the shared deploy
// key alone cannot make it run arbitrary code.
type DeployWebhookPayload struct {
	EventType   string `json:"event_type"` // release | main | branch
	Ref         string `json:"ref"`        // branch or tag name
	Commit      string `json:"commit"`     // short commit hash
	BuildDate   string `json:"build_date"`
	Version     string `json:"version"` // human-facing version of the incoming build
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"` // hex digest of the binary at DownloadURL
	// Target is the environment CI intends this for (production | staging). The
	// server also enforces its own env, so a mismatched target is ignored — this
	// is a belt-and-suspenders guard against a staging event reaching prod.
	Target string `json:"target"`
	// Env is the configuration delivered from the GitHub Environment this deploy
	// ran in (see .github/actions/collect-deploy-env): the server stores it and
	// applies it to its own process environment on the way back up, so config
	// and secrets are managed in GitHub rather than on each box. Only keys
	// envstore allows are accepted — see its trust model.
	Env map[string]string `json:"env"`
	// EnvSecretKeys names the entries of Env that came from GitHub *secrets*
	// rather than plain variables, so the agent sandbox can scrub exactly those
	// from the untrusted shell's environment instead of guessing from the name.
	EnvSecretKeys []string `json:"env_secret_keys"`
}

// GetVersion returns the running build (for the UI version footer). Behind auth.
func (api *API) GetVersion(w http.ResponseWriter, r *http.Request) {
	if api.updater == nil {
		api.respondJSON(w, http.StatusOK, map[string]string{
			"version": "dev", "branch": "dev", "commit_hash": "unknown",
			"build_date": "unknown", "display": "dev",
		})
		return
	}
	v := api.updater.Current()
	api.respondJSON(w, http.StatusOK, map[string]string{
		"version":     v.Version,
		"branch":      v.Branch,
		"commit_hash": v.CommitHash,
		"build_date":  v.BuildDate,
		"display":     v.DisplayString(),
	})
}

// GetDeployStatus returns the deploy state + this server's environment and
// effective source setting, for the UI's Deployment panel. Behind auth.
func (api *API) GetDeployStatus(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	resp := map[string]interface{}{
		"environment":   utils.DeployEnv(),
		"deploy_source": settings.EffectiveDeploySource(),
		"auto_deploy":   settings.AutoDeploy,
	}
	if api.updater != nil {
		st := api.updater.GetStatus()
		resp["current"] = st.Current
		resp["deploying"] = st.Deploying
		if st.Deploying && st.LastDeploy != nil {
			resp["deploy_target"] = st.LastDeploy
		}
		// A deploy failure message can carry internal detail (download URLs,
		// filesystem paths), so it goes only to the operator — same gate as the
		// instance-global fields in GetSettings.
		if st.LastError != "" && (utils.IsE2E() || globalAdminAPIEnabled()) {
			resp["last_error"] = st.LastError
		}
	}
	// The NAMES of the variables the last deploy delivered — never the values —
	// so an operator can confirm configuration arrived without shell access to
	// the box. Same operator-only gate as last_error: the list of secret names a
	// server holds is itself a hint worth not publishing.
	if utils.IsE2E() || globalAdminAPIEnabled() {
		if store, err := envstore.Load(); err == nil {
			resp["env_keys"] = store.Keys()
			resp["env_updated_at"] = store.UpdatedAt
		}
	}
	api.respondJSON(w, http.StatusOK, resp)
}

// DeployWebhook is the PUBLIC endpoint CI calls to trigger a deploy. It is not
// behind the session auth middleware (CI has no user session); it authenticates
// with the shared HEADCOUNT1_DEPLOY_API_KEY instead. When the event matches
// this server's environment + settings, it downloads the new binary and
// restarts into it (see updater.Deploy); otherwise it acknowledges the event
// without acting, so CI can broadcast the same event to every environment.
func (api *API) DeployWebhook(w http.ResponseWriter, r *http.Request) {
	key := utils.DeployAPIKey()
	if key == "" {
		// No key configured → the deploy surface is intentionally closed.
		api.respondError(w, http.StatusNotFound, "not found")
		return
	}
	presented := r.Header.Get("X-Deploy-Key")
	if subtle.ConstantTimeCompare([]byte(presented), []byte(key)) != 1 {
		api.respondError(w, http.StatusUnauthorized, "invalid deploy key")
		return
	}

	var payload DeployWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	// Reject a malformed/disallowed artifact reference up front, with a real
	// error, rather than letting it fail asynchronously inside Deploy where only
	// the server log would show it. Deploy re-validates regardless.
	if err := updater.ValidateDownloadURL(payload.DownloadURL); err != nil {
		api.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(payload.SHA256) == "" {
		api.respondError(w, http.StatusBadRequest, "sha256 is required")
		return
	}
	// Refuse a bad env key loudly, before deciding whether to act: a mistake in
	// the environment's DEPLOY_ENV_KEYS should fail the CI job with the reason,
	// not be silently dropped on the floor. Rejecting the whole payload (rather
	// than the offending keys) keeps "the deploy succeeded" from meaning "most of
	// your configuration arrived".
	if problems := envstore.Check(payload.Env); len(problems) > 0 {
		api.respondError(w, http.StatusBadRequest,
			"rejected delivered env: "+strings.Join(problems, "; "))
		return
	}

	settings := LoadSettings()
	decision, reason := deployDecision(payload, utils.DeployEnv(), settings)

	if !decision {
		// Acknowledge without acting — an expected outcome for events meant for
		// another environment or a different source setting.
		api.respondJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": reason})
		return
	}

	if api.updater == nil {
		api.respondError(w, http.StatusServiceUnavailable, "updater not available")
		return
	}

	// Store the delivered configuration before the deploy starts, so the binary
	// we're about to exec into reads the new values on its way up. Only for
	// events this server acts on — an event aimed at another environment must
	// never reconfigure this one.
	envChanged, err := persistDeliveredEnv(payload)
	if err != nil {
		// The configuration is the deploy's payload as much as the binary is;
		// shipping the new build against stale config would be worse than not
		// deploying, so this fails the CI job instead.
		api.respondError(w, http.StatusInternalServerError, "could not store delivered env: "+err.Error())
		return
	}

	target := updater.VersionInfo{
		Version:    payload.Version,
		Branch:     payload.Ref,
		CommitHash: payload.Commit,
		BuildDate:  payload.BuildDate,
	}
	if api.updater.IsCurrent(target) {
		// Same build — but env is read once at startup, so new configuration for
		// the commit we're already on still needs a cycle to take effect.
		if envChanged {
			go func() {
				if err := api.updater.RestartInPlace("delivered configuration changed"); err != nil {
					log.Printf("Deploy: restart for env change failed: %v", err)
				}
			}()
			api.respondJSON(w, http.StatusAccepted, map[string]string{
				"status": "restarting", "reason": "delivered configuration changed",
			})
			return
		}
		api.respondJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "already running this commit"})
		return
	}

	// Kick the deploy off in the background and respond immediately: Deploy
	// replaces the binary and signals this process to shut down, so we must
	// answer the webhook before that shutdown races the response out.
	go func() {
		if err := api.updater.Deploy(payload.DownloadURL, payload.SHA256, target); err != nil {
			// Deploy records the error in its status; nothing else to do here
			// (the webhook has already been answered).
			_ = err
		}
	}()

	api.respondJSON(w, http.StatusAccepted, map[string]string{
		"status": "deploying",
		"target": target.DisplayString(),
	})
}

// persistDeliveredEnv writes the payload's configuration to the env store and
// reports whether it differs from what was already there.
//
// An ABSENT env field means "this deploy does not manage configuration" and
// leaves the store untouched, so a hand-rolled deploy (curl, an older CI) cannot
// wipe the server's config. An explicitly empty object means "deliver nothing",
// which clears it — that is how removing every key from DEPLOY_ENV_KEYS takes
// effect.
func persistDeliveredEnv(p DeployWebhookPayload) (changed bool, err error) {
	if p.Env == nil {
		return false, nil
	}
	current, err := envstore.Load()
	if err != nil {
		// A store we can't read is a store we can't compare against; overwrite it
		// with what CI just told us, which is the authoritative copy anyway.
		log.Printf("Deploy: unreadable env store, replacing it: %v", err)
		current = envstore.Store{}
	}
	incoming := envstore.Store{Values: p.Env, SecretKeys: p.EnvSecretKeys}
	if incoming.Digest() == current.Digest() {
		return false, nil
	}
	if err := envstore.Save(incoming); err != nil {
		return false, err
	}
	log.Printf("Deploy: stored %d delivered env vars: %s",
		len(incoming.Values), strings.Join(incoming.Keys(), ", "))
	return true, nil
}

// deployDecision decides whether a server in environment `env` with the given
// settings should apply this event. Returns (apply, human-readable reason).
//
//   - Staging applies only branch events (any feature/PR branch); main/release
//     events belong to production.
//   - Production applies only main/release events, and only the one matching
//     the configured deploy_source ("releases" → release events, "main" → main
//     push events). Branch events are ignored.
//   - AutoDeploy=false pauses all applying regardless of match.
//   - A payload Target that names a different environment is always ignored.
func deployDecision(p DeployWebhookPayload, env string, settings Settings) (bool, string) {
	if p.Target != "" && p.Target != env {
		return false, "event targets " + p.Target + ", this server is " + env
	}
	if !settings.AutoDeploy {
		return false, "auto-deploy is disabled on this server"
	}

	if env == utils.EnvStaging {
		if p.EventType == deployEventBranch {
			return true, "staging deploys branch " + p.Ref
		}
		return false, "staging only auto-deploys branch/PR events"
	}

	// Production.
	switch settings.EffectiveDeploySource() {
	case appsettings.DeploySourceMain:
		if p.EventType == deployEventMain {
			return true, "production tracking main: deploying main push"
		}
		return false, "production tracks main; ignoring " + p.EventType + " event"
	default: // releases
		if p.EventType == deployEventRelease {
			return true, "production tracking releases: deploying release " + p.Ref
		}
		return false, "production tracks releases; ignoring " + p.EventType + " event"
	}
}
