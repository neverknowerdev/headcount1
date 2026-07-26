package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/utils"

	"github.com/stretchr/testify/require"
)

func TestDeployDecision(t *testing.T) {
	releasesProd := appsettings.Settings{DeploySource: appsettings.DeploySourceReleases, AutoDeploy: true}
	mainProd := appsettings.Settings{DeploySource: appsettings.DeploySourceMain, AutoDeploy: true}
	paused := appsettings.Settings{DeploySource: appsettings.DeploySourceReleases, AutoDeploy: false}

	cases := []struct {
		name     string
		payload  DeployWebhookPayload
		env      string
		settings appsettings.Settings
		want     bool
	}{
		// Production tracking releases.
		{"prod releases <- release", DeployWebhookPayload{EventType: deployEventRelease}, utils.EnvProduction, releasesProd, true},
		{"prod releases ignores main", DeployWebhookPayload{EventType: deployEventMain}, utils.EnvProduction, releasesProd, false},
		{"prod releases ignores branch", DeployWebhookPayload{EventType: deployEventBranch}, utils.EnvProduction, releasesProd, false},

		// Production tracking main.
		{"prod main <- main", DeployWebhookPayload{EventType: deployEventMain}, utils.EnvProduction, mainProd, true},
		{"prod main ignores release", DeployWebhookPayload{EventType: deployEventRelease}, utils.EnvProduction, mainProd, false},

		// Staging takes any branch, but not main/release (those are prod's).
		{"staging <- branch", DeployWebhookPayload{EventType: deployEventBranch}, utils.EnvStaging, releasesProd, true},
		{"staging ignores main", DeployWebhookPayload{EventType: deployEventMain}, utils.EnvStaging, releasesProd, false},
		{"staging ignores release", DeployWebhookPayload{EventType: deployEventRelease}, utils.EnvStaging, releasesProd, false},

		// Auto-deploy paused: nothing applies.
		{"paused ignores matching release", DeployWebhookPayload{EventType: deployEventRelease}, utils.EnvProduction, paused, false},

		// Target mismatch is always ignored, even for an otherwise-matching event.
		{"target mismatch ignored", DeployWebhookPayload{EventType: deployEventRelease, Target: utils.EnvStaging}, utils.EnvProduction, releasesProd, false},
		{"target match honoured", DeployWebhookPayload{EventType: deployEventRelease, Target: utils.EnvProduction}, utils.EnvProduction, releasesProd, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := deployDecision(tc.payload, tc.env, tc.settings)
			require.Equal(t, tc.want, got, "reason: %s", reason)
		})
	}
}

func TestDeployWebhookAuth(t *testing.T) {
	api := &API{}
	post := func(key string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/deploy/webhook", bytes.NewReader(b))
		if key != "" {
			r.Header.Set("X-Deploy-Key", key)
		}
		w := httptest.NewRecorder()
		api.DeployWebhook(w, r)
		return w
	}

	validBody := DeployWebhookPayload{
		EventType:   deployEventBranch,
		DownloadURL: "https://api.github.com/repos/neverknowerdev/headcount1/releases/assets/1",
		SHA256:      "abc123",
	}

	// No key configured on the server: the endpoint is hidden entirely.
	require.Equal(t, http.StatusNotFound, post("secret", validBody).Code)

	t.Setenv("HEADCOUNT1_DEPLOY_API_KEY", "secret")

	// Wrong / missing key is rejected.
	require.Equal(t, http.StatusUnauthorized, post("", validBody).Code)
	require.Equal(t, http.StatusUnauthorized, post("nope", validBody).Code)

	// Correct key + a payload that doesn't match this (production) server's
	// source is accepted-but-ignored, proving auth passed without needing a
	// real binary/restart. (Default env is production, default source releases;
	// a branch event is ignored.)
	w := post("secret", validBody)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "ignored", resp["status"])
}

// TestDeployWebhookRejectsUntrustedArtifact ensures the webhook refuses a
// deploy whose artifact it cannot trust, with a real 400 — rather than
// accepting it and only failing asynchronously in the updater. Authentication
// alone must not be enough to make the server run arbitrary code.
func TestDeployWebhookRejectsUntrustedArtifact(t *testing.T) {
	t.Setenv("HEADCOUNT1_DEPLOY_API_KEY", "secret")
	api := &API{}
	post := func(body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/deploy/webhook", bytes.NewReader(b))
		r.Header.Set("X-Deploy-Key", "secret")
		w := httptest.NewRecorder()
		api.DeployWebhook(w, r)
		return w
	}
	const ourAsset = "https://api.github.com/repos/neverknowerdev/headcount1/releases/assets/1"

	// A binary hosted anywhere but our own release assets.
	require.Equal(t, http.StatusBadRequest, post(DeployWebhookPayload{
		EventType: deployEventBranch, DownloadURL: "https://evil.example.com/backdoor", SHA256: "abc123",
	}).Code)

	// Someone else's repository on GitHub.
	require.Equal(t, http.StatusBadRequest, post(DeployWebhookPayload{
		EventType: deployEventBranch, DownloadURL: "https://api.github.com/repos/attacker/evil/releases/assets/1", SHA256: "abc123",
	}).Code)

	// Missing URL, and missing digest (which would otherwise allow deploying any
	// artifact ever published, including an old vulnerable build).
	require.Equal(t, http.StatusBadRequest, post(DeployWebhookPayload{
		EventType: deployEventBranch, SHA256: "abc123",
	}).Code)
	require.Equal(t, http.StatusBadRequest, post(DeployWebhookPayload{
		EventType: deployEventBranch, DownloadURL: ourAsset,
	}).Code)
}
