package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSecretsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}, &db.MCPServer{}, &db.MCPAccount{}))
	return database
}

// TestSecretsEncryptedAtRest verifies the full contract of secret storage:
// ciphertext in the DB column, plaintext in memory, and no secret in the
// JSON the API would send to a client.
func TestSecretsEncryptedAtRest(t *testing.T) {
	database := setupSecretsTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	created, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "test", BaseUrl: "https://api.example.com", ApiKey: "sk-raw-secret",
	})
	require.NoError(t, err)
	assert.True(t, created.HasApiKey)

	// The column must hold ciphertext, never the raw key.
	var raw string
	require.NoError(t, database.Raw("SELECT api_key FROM llm_providers WHERE id = ?", created.ID).Scan(&raw).Error)
	assert.True(t, secrets.IsSealed(raw), "api_key column not sealed: %q", raw)
	assert.NotContains(t, raw, "sk-raw-secret")

	// Reads through GORM transparently decrypt.
	got, err := q.GetLLMProvider(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-raw-secret", got.ApiKey)
	assert.True(t, got.HasApiKey)

	// The API serialization (respondJSON marshals this struct) must expose
	// only the presence flag, never the key itself.
	body, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "sk-raw-secret")
	assert.NotContains(t, string(body), `"api_key":`)
	assert.Contains(t, string(body), `"has_api_key":true`)

	// Same contract for MCP account tokens.
	acc, err := q.CreateMCPAccount(ctx, db.MCPAccount{MCPServerID: 1, Name: "Default", AuthToken: "ghp_raw_token"})
	require.NoError(t, err)
	require.NoError(t, database.Raw("SELECT auth_token FROM mcp_accounts WHERE id = ?", acc.ID).Scan(&raw).Error)
	assert.True(t, secrets.IsSealed(raw))
	gotAcc, err := q.GetMCPAccount(ctx, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ghp_raw_token", gotAcc.AuthToken)
	assert.True(t, gotAcc.HasToken)
}

// TestUserOwnedSecretsSealedWithUserDEK verifies the serializer routes secrets
// on user-owned rows to the owner's DEK ("enc:u1:<id>:"), that they decrypt
// transparently, and that deleting the user's key crypto-shreds them.
func TestUserOwnedSecretsSealedWithUserDEK(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&db.User{}, &db.UserKey{}, &db.LLMProvider{}))
	secrets.SetUserKeyStorage(db.NewUserKeyStorage(database))
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "owner@example.com", "irrelevant-hash")
	require.NoError(t, err)
	require.NoError(t, secrets.Default().CreateUserKey(user.ID, "owner-password"))

	created, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "mine", BaseUrl: "https://u", ApiKey: "sk-user-owned", UserID: &user.ID,
	})
	require.NoError(t, err)

	var raw string
	require.NoError(t, database.Raw("SELECT api_key FROM llm_providers WHERE id = ?", created.ID).Scan(&raw).Error)
	assert.True(t, strings.HasPrefix(raw, secrets.PrefixUser), "expected user-sealed value, got %q", raw)
	assert.NotContains(t, raw, "sk-user-owned")

	got, err := q.GetLLMProvider(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-user-owned", got.ApiKey)

	// Crypto-shredding: dropping the user's key makes the row unreadable.
	require.NoError(t, q.DeleteUserKey(ctx, user.ID))
	secrets.SetUserKeyStorage(db.NewUserKeyStorage(database)) // reset the wrapped-key cache
	_, err = q.GetLLMProvider(ctx, created.ID)
	require.Error(t, err)
}

// TestEncryptPlaintextSecretsSweep verifies the startup migration seals rows
// written before encryption existed, and is idempotent.
func TestEncryptPlaintextSecretsSweep(t *testing.T) {
	database := setupSecretsTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	// Simulate a pre-encryption install: raw values inserted outside GORM.
	require.NoError(t, database.Exec(
		"INSERT INTO llm_providers (name, base_url, api_key) VALUES ('legacy', 'https://u', 'sk-legacy')",
	).Error)
	require.NoError(t, database.Exec(
		"INSERT INTO mcp_accounts (mcp_server_id, name, auth_token) VALUES (1, 'Default', 'tok-legacy')",
	).Error)

	require.NoError(t, q.EncryptPlaintextSecrets(ctx))

	var rawKey, rawTok string
	require.NoError(t, database.Raw("SELECT api_key FROM llm_providers WHERE name = 'legacy'").Scan(&rawKey).Error)
	require.NoError(t, database.Raw("SELECT auth_token FROM mcp_accounts WHERE name = 'Default'").Scan(&rawTok).Error)
	assert.True(t, secrets.IsSealed(rawKey), "sweep left provider key raw: %q", rawKey)
	assert.True(t, secrets.IsSealed(rawTok), "sweep left mcp token raw: %q", rawTok)

	// Values still round-trip after the sweep, and a second run is a no-op.
	require.NoError(t, q.EncryptPlaintextSecrets(ctx))
	var p db.LLMProvider
	require.NoError(t, database.Where("name = 'legacy'").First(&p).Error)
	assert.Equal(t, "sk-legacy", p.ApiKey)
	var a db.MCPAccount
	require.NoError(t, database.Where("name = 'Default'").First(&a).Error)
	assert.Equal(t, "tok-legacy", a.AuthToken)

	// Providers seeded with an empty key (builtin free providers) are left
	// untouched — empty must stay empty, not become ciphertext.
	require.NoError(t, database.Exec(
		"INSERT INTO llm_providers (name, base_url, api_key) VALUES ('blank', 'https://u', '')",
	).Error)
	require.NoError(t, q.EncryptPlaintextSecrets(ctx))
	var rawBlank string
	require.NoError(t, database.Raw("SELECT api_key FROM llm_providers WHERE name = 'blank'").Scan(&rawBlank).Error)
	assert.Equal(t, "", rawBlank)
}
