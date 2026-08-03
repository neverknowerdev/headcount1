package endpoints

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForwardedGitHubWebhookSignature(t *testing.T) {
	body := []byte(`{"repository":{"id":42}}`)
	secret := "shared-relay-secret"
	signature := forwardedWebhookSignature(body, secret)

	require.True(t, validForwardedWebhook(body, signature, secret))
	require.False(t, validForwardedWebhook([]byte(`{"repository":{"id":43}}`), signature, secret))
	require.False(t, validForwardedWebhook(body, signature, "wrong-secret"))
	require.False(t, validForwardedWebhook(body, "", secret))
}
