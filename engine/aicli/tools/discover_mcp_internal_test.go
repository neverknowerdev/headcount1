package tools

import (
	"context"
	"testing"

	"agent-orchestrator/db"
	"github.com/stretchr/testify/require"
)

func TestRefreshServerAuthReplacesShortLivedToken(t *testing.T) {
	store := NewMCPSessionStore([]db.MCPServer{{
		Name: "github/work", Transport: "stdio", Command: "github-mcp-server", AuthToken: "expired-token",
	}}, nil, nil)
	store.SetAuthTokenRefresher(func(_ context.Context, serverName string) (string, error) {
		require.Equal(t, "github/work", serverName)
		return "renewed-installation-token", nil
	})

	require.NoError(t, store.refreshServerAuth(context.Background(), "github/work"))
	require.Equal(t, "renewed-installation-token", store.servers["github/work"].AuthToken)
}

func TestRefreshServerAuthDoesNotAcceptEmptyToken(t *testing.T) {
	store := NewMCPSessionStore([]db.MCPServer{{Name: "github/work", Transport: "stdio", Command: "github-mcp-server"}}, nil, nil)
	store.SetAuthTokenRefresher(func(context.Context, string) (string, error) { return "", nil })

	require.Error(t, store.refreshServerAuth(context.Background(), "github/work"))
}
