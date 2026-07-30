package db_test

import (
	"context"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupEnvTest(t *testing.T) (*db.Queries, *gorm.DB, db.Company, db.User) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.User{}, &db.Company{}, &db.Environment{}, &db.EnvironmentSecret{},
		&db.Task{}, &db.Sprint{}, &db.Project{}, &db.Agent{},
	))
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "env@test.local")
	require.NoError(t, err)
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)
	t.Cleanup(func() { secrets.Default().LockUser(user.ID) })

	company := db.Company{Name: "EnvCo", ShortName: "ENV"}
	require.NoError(t, database.Create(&company).Error)
	return q, database, company, user
}

func TestEnsureDefaultEnvironmentsSeedsOnce(t *testing.T) {
	q, _, company, _ := setupEnvTest(t)
	ctx := context.Background()

	envs, err := q.ListEnvironments(ctx, company.ID)
	require.NoError(t, err)
	require.Len(t, envs, 3)

	names := map[string]db.Environment{}
	for _, e := range envs {
		names[e.Name] = e
		require.True(t, e.Builtin)
	}
	require.Contains(t, names, "headcount1 cloud")
	require.Contains(t, names, "preview")
	require.Contains(t, names, "production")
	require.True(t, names["headcount1 cloud"].IsDefault)
	require.False(t, names["preview"].IsDefault)

	// Idempotent — a second listing doesn't duplicate.
	envs, err = q.ListEnvironments(ctx, company.ID)
	require.NoError(t, err)
	require.Len(t, envs, 3)

	def, err := q.GetDefaultEnvironment(ctx, company.ID)
	require.NoError(t, err)
	require.Equal(t, "headcount1 cloud", def.Name)
}

func TestEnvironmentCRUDAndBuiltinProtection(t *testing.T) {
	q, _, company, _ := setupEnvTest(t)
	ctx := context.Background()

	env, err := q.CreateEnvironment(ctx, company.ID, "staging")
	require.NoError(t, err)
	require.False(t, env.Builtin)

	renamed, err := q.RenameEnvironment(ctx, env.ID, "staging-eu")
	require.NoError(t, err)
	require.Equal(t, "staging-eu", renamed.Name)

	def, err := q.GetDefaultEnvironment(ctx, company.ID)
	require.NoError(t, err)
	_, err = q.RenameEnvironment(ctx, def.ID, "nope")
	require.Error(t, err, "builtin environments must not be renameable")
	require.Error(t, q.DeleteEnvironment(ctx, def.ID), "builtin environments must not be deletable")

	require.NoError(t, q.DeleteEnvironment(ctx, env.ID))
	envs, err := q.ListEnvironments(ctx, company.ID)
	require.NoError(t, err)
	require.Len(t, envs, 3)
}

func TestEnvironmentSecretSealedAtRest(t *testing.T) {
	q, database, company, user := setupEnvTest(t)
	ctx := context.Background()

	env, err := q.GetDefaultEnvironment(ctx, company.ID)
	require.NoError(t, err)

	const value = "env-secret-plaintext-value-42"
	row, err := q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "API_KEY", value)
	require.NoError(t, err)
	require.True(t, row.HasValue)

	// Stored sealed (enc:u1), never raw.
	var raw string
	require.NoError(t, database.Raw("SELECT value FROM environment_secrets WHERE environment_id = ? AND name = ?", env.ID, "API_KEY").Scan(&raw).Error)
	require.True(t, secrets.IsSealed(raw), "value must be sealed: %q", raw)
	require.NotContains(t, raw, value)

	// Round-trips through Decrypt while unlocked.
	rows, err := q.ListEnvironmentSecrets(ctx, env.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	plain, err := secrets.Default().Decrypt(rows[0].ValueEncrypted)
	require.NoError(t, err)
	require.Equal(t, value, plain)

	// Upsert replaces in place (same environment+name).
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "API_KEY", "rotated-value")
	require.NoError(t, err)
	rows, _ = q.ListEnvironmentSecrets(ctx, env.ID)
	require.Len(t, rows, 1)
	plain, _ = secrets.Default().Decrypt(rows[0].ValueEncrypted)
	require.Equal(t, "rotated-value", plain)

	// Locked vault → write fails with ErrLocked (never silently unsealed).
	secrets.Default().LockUser(user.ID)
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "OTHER", "v")
	require.ErrorIs(t, err, secrets.ErrLocked)

	// Invalid env var names are rejected.
	secretsDekRelock(t, user.ID)
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "BAD NAME", "v")
	require.Error(t, err)
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "1LEADING", "v")
	require.Error(t, err)

	require.NoError(t, q.DeleteEnvironmentSecret(ctx, env.ID, "API_KEY"))
	rows, _ = q.ListEnvironmentSecrets(ctx, env.ID)
	require.Empty(t, rows)
}

func secretsDekRelock(t *testing.T, userID int32) {
	t.Helper()
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(userID, dek, time.Minute)
}

// TestDefaultEnvironmentSecrets: task runs always draw from the company's
// default environment ("headcount1 cloud") — other environments' secrets
// stay out of the run path entirely.
func TestDefaultEnvironmentSecrets(t *testing.T) {
	q, _, company, user := setupEnvTest(t)
	ctx := context.Background()

	staging, err := q.CreateEnvironment(ctx, company.ID, "staging")
	require.NoError(t, err)
	def, err := q.GetDefaultEnvironment(ctx, company.ID)
	require.NoError(t, err)

	_, err = q.UpsertEnvironmentSecret(ctx, def.ID, user.ID, "DEFAULT_ONLY", "dv")
	require.NoError(t, err)
	_, err = q.UpsertEnvironmentSecret(ctx, staging.ID, user.ID, "STAGING_ONLY", "sv")
	require.NoError(t, err)

	env, rows, err := q.DefaultEnvironmentSecrets(ctx, company.ID)
	require.NoError(t, err)
	require.Equal(t, "headcount1 cloud", env.Name)
	require.Len(t, rows, 1)
	require.Equal(t, "DEFAULT_ONLY", rows[0].Name)

	// Deleting a non-default environment doesn't disturb the default's set.
	require.NoError(t, q.DeleteEnvironment(ctx, staging.ID))
	env, rows, err = q.DefaultEnvironmentSecrets(ctx, company.ID)
	require.NoError(t, err)
	require.Equal(t, "headcount1 cloud", env.Name)
	require.Len(t, rows, 1)
}
