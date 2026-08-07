package endpoints

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/authctx"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGoogleOAuthIsRestrictedToGoogleDocsIntegration(t *testing.T) {
	require.True(t, isGoogleOAuthServer(db.MCPServer{Name: "google-docs", AuthType: "google-oauth"}))
	require.False(t, isGoogleOAuthServer(db.MCPServer{Name: "github", AuthType: "github-app"}))
	require.False(t, isGoogleOAuthServer(db.MCPServer{Name: "google-docs", AuthType: "url-token"}))

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	api := NewAPI(database, nil, nil)
	user := db.User{ID: 1}
	server := db.MCPServer{ID: 9, Name: "github", AuthType: "github-app"}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"account_name":"test","oauth_keys_json":"{}"}`))
	ctx := context.WithValue(authctx.WithUser(request.Context(), user), mcpServerKey, server)
	recorder := httptest.NewRecorder()
	api.StartGoogleOAuth(recorder, request.WithContext(ctx))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestGoogleOAuthAccountMustBelongToSelectedServerAndUser(t *testing.T) {
	userID := int32(7)
	google := db.MCPServer{ID: 3, Name: "google-docs", AuthType: "google-oauth"}
	require.True(t, googleOAuthAccountMatches(google, db.MCPAccount{MCPServerID: google.ID, UserID: &userID}, userID))
	require.False(t, googleOAuthAccountMatches(google, db.MCPAccount{MCPServerID: 4, UserID: &userID}, userID))
	otherUser := int32(8)
	require.False(t, googleOAuthAccountMatches(google, db.MCPAccount{MCPServerID: google.ID, UserID: &otherUser}, userID))
}
