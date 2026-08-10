package endpoints

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

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
	// Env is every variable and secret the GitHub Environment this deploy ran in
	// exposes (see .github/actions/collect-deploy-env): the server stores it and
	// applies it to its own process environment on the way back up, so config and
	// secrets are managed in GitHub rather than on each box. Names envstore
	// refuses are skipped and reported in the response, not fatal — see its trust
	// model.
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

// bootKeyState records how the boot key resolved on THIS boot (see
// pkg/bootkey): which backend is in effect, whether a graceful-exit keyring
// snapshot was waiting, and how many vaults it re-warmed. Set once from main at
// startup and read-only afterwards, so a plain value behind a mutex is enough.
type bootKeyState struct {
	Backend        string `json:"backend"` // "none" when restarts require a re-tap
	SnapshotFound  bool   `json:"snapshot_found"`
	RestoredVaults int    `json:"restored_vaults"`
}

var (
	bootKeyMu     sync.RWMutex
	bootKeyStatus = bootKeyState{Backend: "none"}
)

// SetBootKeyStatus publishes this boot's boot-key outcome for the Deployment
// panel. Called by main after the restore attempt.
func SetBootKeyStatus(backend string, snapshotFound bool, restoredVaults int) {
	bootKeyMu.Lock()
	defer bootKeyMu.Unlock()
	bootKeyStatus = bootKeyState{Backend: backend, SnapshotFound: snapshotFound, RestoredVaults: restoredVaults}
}

// GetDeployStatus returns the deploy state + this server's environment and
// effective source setting, for the UI's Deployment panel. Behind auth.
func (api *API) GetDeployStatus(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	admin := api.isInstanceAdmin(r.Context(), api.currentUserID(r))
	resp := map[string]interface{}{
		"environment":   utils.CurrentEnv(),
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
		if st.LastError != "" && admin {
			resp["last_error"] = st.LastError
		}
	}
	// The NAMES of the variables the last deploy delivered — never the values —
	// so an operator can confirm configuration arrived without shell access to
	// the box. Same operator-only gate as last_error: the list of secret names a
	// server holds is itself a hint worth not publishing.
	if admin {
		if store, err := envstore.Load(); err == nil {
			resp["env_key_names"] = store.Keys()
			resp["env_updated_at"] = store.UpdatedAt
		}
		// Whether a deploy restart re-warms unlocked vaults or makes everyone
		// re-tap their passkey, and — when it didn't — which half failed: no boot
		// key at all, or a boot key with no snapshot waiting for it. Operator-only
		// for the same reason as the rest of this block; the backend name can name
		// a path or a Vault key.
		bootKeyMu.RLock()
		resp["boot_key"] = bootKeyStatus
		bootKeyMu.RUnlock()
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
	// An implausible number of variables means something is wrong upstream, and
	// silently truncating configuration would be worse than not deploying.
	if len(payload.Env) > envstore.MaxKeys {
		api.respondError(w, http.StatusBadRequest,
			fmt.Sprintf("too many env vars: %d (max %d)", len(payload.Env), envstore.MaxKeys))
		return
	}

	settings := LoadSettings()
	decision, reason := deployDecision(payload, utils.CurrentEnv(), settings)

	if !decision {
		// Acknowledge without acting — an expected outcome for events meant for
		// another environment or a different source setting.
		api.respondJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": reason})
		return
	}

	// Store the delivered configuration before the deploy starts, so the binary
	// we're about to exec into reads the new values on its way up. Only for
	// events this server acts on — an event aimed at another environment must
	// never reconfigure this one.
	//
	// Ahead of the updater check on purpose: configuration is worth persisting
	// even on a server that can't restart itself, since the next start applies it.
	envChanged, skipped, err := persistDeliveredEnv(payload)
	if err != nil {
		// The configuration is the deploy's payload as much as the binary is;
		// shipping the new build against stale config would be worse than not
		// deploying, so this fails the CI job instead.
		api.respondError(w, http.StatusInternalServerError, "could not store delivered env: "+err.Error())
		return
	}
	// Echo the names that could not be applied. CI delivers everything the
	// environment holds, so some are expected — but they must show up in the job
	// log rather than vanishing.
	respond := func(code int, body map[string]interface{}) {
		if len(skipped) > 0 {
			body["env_skipped"] = envstore.SkippedSummary(skipped)
		}
		api.respondJSON(w, code, body)
	}

	if api.updater == nil {
		// Through respond, not respondError: the configuration was still stored,
		// so CI should hear which parts of it didn't make it.
		respond(http.StatusServiceUnavailable, map[string]interface{}{"error": "updater not available"})
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
			if err := api.updater.RestartInPlace("delivered configuration changed"); err != nil {
				respond(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
				return
			}
			respond(http.StatusAccepted, map[string]interface{}{
				"status": "restarting", "reason": "delivered configuration changed",
			})
			return
		}
		// This is also how CI confirms a deploy finished: it re-posts the same
		// event after a 202 and reads this answer — the new binary reporting
		// itself current IS the success signal.
		respond(http.StatusOK, map[string]interface{}{
			"status": "ignored", "reason": "already running this commit",
		})
		return
	}

	// Deploy runs SYNCHRONOUSLY so a failure (bad download, digest mismatch,
	// swap error) comes back as this request's status code and shows up in the
	// CI job log, instead of only in the server's own log. That is safe because
	// a successful Deploy does not kill the process from under the response: it
	// arms a restart and requests a graceful shutdown after a grace delay, and
	// the HTTP server drains in-flight responses before exiting. What CANNOT be
	// awaited here is the restart itself — the process has to die to complete
	// it — so success is still a 202, and CI confirms completion by re-posting
	// the event until the new binary answers "already running this commit".
	if err := api.updater.Deploy(payload.DownloadURL, payload.SHA256, target); err != nil {
		if errors.Is(err, updater.ErrInProgress) {
			// Not a failure: an earlier deploy holds the process. CI polls until
			// that one lands.
			respond(http.StatusConflict, map[string]interface{}{"error": err.Error()})
			return
		}
		respond(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}

	respond(http.StatusAccepted, map[string]interface{}{
		"status": "deploying",
		"target": target.DisplayString(),
	})
}

// persistDeliveredEnv writes the payload's configuration to the env store and
// reports whether it differs from what was already there, plus any names that
// could not be applied (with the reason).
//
// An ABSENT env field means "this deploy does not manage configuration" and
// leaves the store untouched, so a hand-rolled deploy (curl, an older CI) cannot
// wipe the server's config. An explicitly empty object means "deliver nothing",
// which clears it — that is how emptying the GitHub Environment takes effect.
func persistDeliveredEnv(p DeployWebhookPayload) (changed bool, skipped map[string]string, err error) {
	if p.Env == nil {
		return false, nil, nil
	}
	accepted, skipped := envstore.Partition(p.Env)
	if len(skipped) > 0 {
		log.Printf("Deploy: skipped %d delivered env vars: %s",
			len(skipped), strings.Join(envstore.SkippedSummary(skipped), "; "))
	}

	current, err := envstore.Load()
	if err != nil {
		// A store we can't read is a store we can't compare against; overwrite it
		// with what CI just told us, which is the authoritative copy anyway.
		log.Printf("Deploy: unreadable env store, replacing it: %v", err)
		current = envstore.Store{}
	}
	// Only names that survived the filter can be marked secret — otherwise the
	// store would claim to be hiding something it never stored.
	secretKeys := make([]string, 0, len(p.EnvSecretKeys))
	for _, k := range p.EnvSecretKeys {
		if _, ok := accepted[k]; ok {
			secretKeys = append(secretKeys, k)
		}
	}

	// Names the previous delivery set that this one no longer carries. Recorded
	// so the next startup actively unsets them — the deploy restart execs with
	// os.Environ(), so a deleted GitHub secret would otherwise survive in the
	// inherited environment (see envstore.Store.RemovedKeys).
	var removed []string
	for key := range current.Values {
		if _, still := accepted[key]; !still {
			removed = append(removed, key)
		}
	}

	incoming := envstore.Store{Values: accepted, SecretKeys: secretKeys, RemovedKeys: removed}
	if incoming.Digest() == current.Digest() {
		return false, skipped, nil
	}
	if err := envstore.Save(incoming); err != nil {
		return false, skipped, err
	}
	log.Printf("Deploy: stored %d delivered env vars: %s",
		len(incoming.Values), strings.Join(incoming.Keys(), ", "))
	return true, skipped, nil
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
