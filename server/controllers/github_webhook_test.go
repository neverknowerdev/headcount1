package endpoints

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type webhookRoundTripper func(*http.Request) (*http.Response, error)

func (f webhookRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestForwardedGitHubWebhookSignature(t *testing.T) {
	body := []byte(`{"repository":{"id":42}}`)
	secret := "shared-relay-secret"
	signature := forwardedWebhookSignature(body, secret)

	require.True(t, validForwardedWebhook(body, signature, secret))
	require.False(t, validForwardedWebhook([]byte(`{"repository":{"id":43}}`), signature, secret))
	require.False(t, validForwardedWebhook(body, signature, "wrong-secret"))
	require.False(t, validForwardedWebhook(body, "", secret))
}

func TestForwardGitHubWebhookPreservesDeliveryID(t *testing.T) {
	body := []byte(`{"repository":{"id":42}}`)
	var gotEvent, gotDelivery, gotSignature string
	client := &http.Client{Transport: webhookRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotEvent = r.Header.Get("X-GitHub-Event")
		gotDelivery = r.Header.Get("X-GitHub-Delivery")
		gotSignature = r.Header.Get(forwardedWebhookSignatureHeader)
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}

	forwardGitHubWebhookTo("https://relay.example/webhook", "relay-secret", body, "workflow_run", "delivery-42", client)
	require.Equal(t, "workflow_run", gotEvent)
	require.Equal(t, "delivery-42", gotDelivery)
	require.True(t, validForwardedWebhook(body, gotSignature, "relay-secret"))
}

func TestGitHubWebhookDeliveryLeaseRejectsConcurrentAndProtectsStaleAttempt(t *testing.T) {
	require.Greater(t, githubWebhookLeaseDuration, githubWebhookWakeTimeout)
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.GitHubWebhookDelivery{}))
	api := NewAPI(database, nil, nil)
	first, complete, err := api.claimGitHubWebhookDelivery(context.Background(), "lease-1", "issue_comment")
	require.NoError(t, err)
	require.False(t, complete)
	_, _, err = api.claimGitHubWebhookDelivery(context.Background(), "lease-1", "issue_comment")
	require.ErrorContains(t, err, "already processing")

	past := time.Now().Add(-time.Second)
	require.NoError(t, database.Model(&db.GitHubWebhookDelivery{}).Where("delivery_id = ?", "lease-1").Update("lease_expires_at", &past).Error)
	second, complete, err := api.claimGitHubWebhookDelivery(context.Background(), "lease-1", "issue_comment")
	require.NoError(t, err)
	require.False(t, complete)
	require.NotEqual(t, first.AttemptToken, second.AttemptToken)
	// The old owner cannot complete a delivery after a stale lease was reclaimed.
	require.ErrorContains(t, api.updateGitHubWebhookDelivery(context.Background(), "lease-1", first.AttemptToken, map[string]any{"status": "completed"}), "lease was lost")
}
