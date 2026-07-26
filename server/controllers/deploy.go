package endpoints

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"agent-orchestrator/pkg/appsettings"
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
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"` // hex digest of the binary at DownloadURL
	// Target is the environment CI intends this for (production | staging). The
	// server also enforces its own env, so a mismatched target is ignored — this
	// is a belt-and-suspenders guard against a staging event reaching prod.
	Target string `json:"target"`
}

// GetVersion returns the running build (for the UI version footer). Behind auth.
func (api *API) GetVersion(w http.ResponseWriter, r *http.Request) {
	if api.updater == nil {
		api.respondJSON(w, http.StatusOK, map[string]string{
			"branch": "dev", "commit_hash": "unknown", "build_date": "unknown", "display": "dev",
		})
		return
	}
	v := api.updater.Current()
	api.respondJSON(w, http.StatusOK, map[string]string{
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

	target := updater.VersionInfo{Branch: payload.Ref, CommitHash: payload.Commit, BuildDate: payload.BuildDate}
	if api.updater.IsCurrent(target) {
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
