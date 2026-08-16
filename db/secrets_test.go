package db_test

import (
	"agent-orchestrator/db/migrations"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return database
}

// TestSecretsStoredAsCiphertext verifies the full contract of secret storage:
// ciphertext in the DB column, ciphertext (never plaintext) on the loaded
// struct, plaintext only via an explicit point-of-use Decrypt*, and no secret
// in the JSON the API would send to a client.
func TestSecretsStoredAsCiphertext(t *testing.T) {
	database := setupSecretsTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "atrest@example.com")
	require.NoError(t, err)
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)
	defer secrets.Default().LockUser(user.ID)

	// The caller (a controller) seals before the value ever lands on the model.
	sealed, err := secrets.Default().EncryptForUser(user.ID, "sk-raw-secret")
	require.NoError(t, err)

	created, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "test", BaseUrl: "https://api.example.com",
		ApiKeyEncrypted: sealed, UserID: &user.ID,
	})
	require.NoError(t, err)
	assert.True(t, created.HasApiKey)

	// The column holds ciphertext, never the raw key.
	var raw string
	require.NoError(t, database.Raw("SELECT api_key FROM llm_providers WHERE id = ?", created.ID).Scan(&raw).Error)
	assert.True(t, secrets.IsSealed(raw), "api_key column not sealed: %q", raw)
	assert.NotContains(t, raw, "sk-raw-secret")

	// A read leaves the field as ciphertext — no transparent decrypt on load.
	got, err := q.GetLLMProvider(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, secrets.IsSealed(got.ApiKeyEncrypted), "loaded struct must hold ciphertext")
	assert.NotContains(t, got.ApiKeyEncrypted, "sk-raw-secret")
	assert.True(t, got.HasApiKey)

	// Only an explicit manager Decrypt, at the point of use, yields the plaintext.
	plain, err := secrets.Default().Decrypt(got.ApiKeyEncrypted)
	require.NoError(t, err)
	assert.Equal(t, "sk-raw-secret", plain)

	// The API serialization (respondJSON marshals this struct) must expose only
	// the presence flag, never the key itself.
	body, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "sk-raw-secret")
	assert.NotContains(t, string(body), `"api_key":`)
	assert.Contains(t, string(body), `"has_api_key":true`)

	// Same contract for MCP account tokens.
	sealedTok, err := secrets.Default().EncryptForUser(user.ID, "ghp_raw_token")
	require.NoError(t, err)
	acc, err := q.CreateMCPAccount(ctx, db.MCPAccount{
		MCPServerID: 1, Name: "Default", AuthTokenEncrypted: sealedTok, UserID: &user.ID,
	})
	require.NoError(t, err)
	require.NoError(t, database.Raw("SELECT auth_token FROM mcp_accounts WHERE id = ?", acc.ID).Scan(&raw).Error)
	assert.True(t, secrets.IsSealed(raw))
	gotAcc, err := q.GetMCPAccount(ctx, acc.ID)
	require.NoError(t, err)
	assert.True(t, secrets.IsSealed(gotAcc.AuthTokenEncrypted))
	tok, err := secrets.Default().Decrypt(gotAcc.AuthTokenEncrypted)
	require.NoError(t, err)
	assert.Equal(t, "ghp_raw_token", tok)
	assert.True(t, gotAcc.HasToken)
}

// TestWriteGuardRejectsUnsealedSecret verifies the serializer refuses to persist
// a plaintext value in a sealed column — a forgotten EncryptForUser fails loudly
// instead of silently writing a secret in the clear.
func TestWriteGuardRejectsUnsealedSecret(t *testing.T) {
	database := setupSecretsTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	_, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "oops", BaseUrl: "https://u", ApiKeyEncrypted: "sk-plaintext-leak",
	})
	require.Error(t, err, "storing an unsealed secret must be rejected")
	assert.Contains(t, err.Error(), "unsealed")
}

// TestUserOwnedSecretsSealedWithUserDEK verifies a user-owned secret is sealed
// under the owner's DEK ("enc:u1:<id>:"), decrypts only for that unlocked user,
// and becomes undecryptable (ErrLocked) once the user is locked.
func TestUserOwnedSecretsSealedWithUserDEK(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "owner@example.com")
	require.NoError(t, err)

	// Unlock the user (as a passkey login would) so their secrets seal/open.
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)

	sealed, err := secrets.Default().EncryptForUser(user.ID, "sk-user-owned")
	require.NoError(t, err)
	created, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "mine", BaseUrl: "https://u", ApiKeyEncrypted: sealed, UserID: &user.ID,
	})
	require.NoError(t, err)

	var raw string
	require.NoError(t, database.Raw("SELECT api_key FROM llm_providers WHERE id = ?", created.ID).Scan(&raw).Error)
	assert.True(t, strings.HasPrefix(raw, secrets.PrefixUser), "expected user-sealed value, got %q", raw)
	assert.NotContains(t, raw, "sk-user-owned")

	got, err := q.GetLLMProvider(ctx, created.ID)
	require.NoError(t, err)
	plain, err := secrets.Default().Decrypt(got.ApiKeyEncrypted)
	require.NoError(t, err)
	assert.Equal(t, "sk-user-owned", plain)

	// Locking the user (logout / crash) makes the secret undecryptable: the row
	// still loads and holds ciphertext, but decrypting returns ErrLocked rather
	// than leaking or blanking the value.
	secrets.Default().LockUser(user.ID)
	locked, err := q.GetLLMProvider(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, secrets.IsSealed(locked.ApiKeyEncrypted))
	_, err = secrets.Default().Decrypt(locked.ApiKeyEncrypted)
	assert.ErrorIs(t, err, secrets.ErrLocked, "locked user's secret must not decrypt")
}

// TestLockedMetadataEditPreservesSecret guards the data-loss bug that motivated
// storing ciphertext on the struct: a metadata-only update (e.g. renaming a
// provider) while the owner's vault is LOCKED must not disturb the stored
// secret. Because the field now carries the ciphertext verbatim (a locked read
// no longer blanks it), a full-struct Save round-trips the sealed value.
func TestLockedMetadataEditPreservesSecret(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "locked-edit@example.com")
	require.NoError(t, err)
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)

	sealedKey, err := secrets.Default().EncryptForUser(user.ID, "sk-must-survive")
	require.NoError(t, err)
	prov, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "orig", BaseUrl: "https://u", ApiKeyEncrypted: sealedKey, UserID: &user.ID,
	})
	require.NoError(t, err)
	sealedTok, err := secrets.Default().EncryptForUser(user.ID, "tok-must-survive")
	require.NoError(t, err)
	acct, err := q.CreateMCPAccount(ctx, db.MCPAccount{
		MCPServerID: 1, Name: "orig", AuthTokenEncrypted: sealedTok, UserID: &user.ID,
	})
	require.NoError(t, err)

	// Lock, load (the field holds ciphertext, NOT ""), change only a metadata
	// field, and Save the full struct back.
	secrets.Default().LockUser(user.ID)

	lockedProv, err := q.GetLLMProvider(ctx, prov.ID)
	require.NoError(t, err)
	require.True(t, secrets.IsSealed(lockedProv.ApiKeyEncrypted)) // held, not blanked
	lockedProv.Name = "renamed"
	_, err = q.UpdateLLMProvider(ctx, lockedProv)
	require.NoError(t, err)

	lockedAcct, err := q.GetMCPAccount(ctx, acct.ID)
	require.NoError(t, err)
	require.True(t, secrets.IsSealed(lockedAcct.AuthTokenEncrypted))
	lockedAcct.Name = "renamed"
	_, err = q.UpdateMCPAccount(ctx, lockedAcct)
	require.NoError(t, err)

	// Re-unlock: the secrets must still be intact (not blanked by the edits).
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)

	gotProv, err := q.GetLLMProvider(ctx, prov.ID)
	require.NoError(t, err)
	assert.Equal(t, "renamed", gotProv.Name, "metadata edit should persist")
	provKey, err := secrets.Default().Decrypt(gotProv.ApiKeyEncrypted)
	require.NoError(t, err)
	assert.Equal(t, "sk-must-survive", provKey, "secret must survive a locked metadata edit")

	gotAcct, err := q.GetMCPAccount(ctx, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, "renamed", gotAcct.Name)
	acctTok, err := secrets.Default().Decrypt(gotAcct.AuthTokenEncrypted)
	require.NoError(t, err)
	assert.Equal(t, "tok-must-survive", acctTok, "token must survive a locked metadata edit")
}
