package db_test

import (
	"context"
	"fmt"
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
		&db.EnvironmentConnector{}, &db.Task{}, &db.Sprint{}, &db.Project{}, &db.Agent{},
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
	require.Len(t, envs, 1, "only the platform env is seeded at company level")
	require.Equal(t, "headcount1 cloud", envs[0].Name)
	require.True(t, envs[0].Builtin)
	require.True(t, envs[0].IsDefault)
	require.Nil(t, envs[0].ProjectID)

	// Idempotent — a second listing doesn't duplicate.
	envs, err = q.ListEnvironments(ctx, company.ID)
	require.NoError(t, err)
	require.Len(t, envs, 1)

	def, err := q.GetDefaultEnvironment(ctx, company.ID)
	require.NoError(t, err)
	require.Equal(t, "headcount1 cloud", def.Name)
}

// TestLegacySeedCleanup: the formerly seeded company-level preview/production
// rows are removed when empty, preserved when they carry entries.
func TestLegacySeedCleanup(t *testing.T) {
	q, database, company, user := setupEnvTest(t)
	ctx := context.Background()

	// Simulate the old seeds.
	empty := db.Environment{CompanyID: company.ID, Name: "preview", Builtin: true}
	adopted := db.Environment{CompanyID: company.ID, Name: "production", Builtin: true}
	require.NoError(t, database.Create(&empty).Error)
	require.NoError(t, database.Create(&adopted).Error)
	_, err := q.UpsertEnvironmentSecret(ctx, adopted.ID, user.ID, "KEEP_ME", "value-123", "")
	require.NoError(t, err)

	envs, err := q.ListEnvironments(ctx, company.ID)
	require.NoError(t, err)
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}
	require.NotContains(t, names, "preview", "empty legacy seed must be cleaned up")
	require.Contains(t, names, "production", "adopted legacy seed must be preserved")
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
	require.Len(t, envs, 1)
}

func TestProjectEnvironmentsAndConnectors(t *testing.T) {
	q, database, company, user := setupEnvTest(t)
	ctx := context.Background()

	project := db.Project{CompanyID: company.ID, Name: "Web App"}
	require.NoError(t, database.Create(&project).Error)
	other := db.Project{CompanyID: company.ID, Name: "API"}
	require.NoError(t, database.Create(&other).Error)

	// Projects start with no environments.
	envs, err := q.ListProjectEnvironments(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, envs)

	// Two projects in one company may both have "production".
	envA, err := q.CreateProjectEnvironment(ctx, company.ID, project.ID, "production")
	require.NoError(t, err)
	_, err = q.CreateProjectEnvironment(ctx, company.ID, other.ID, "production")
	require.NoError(t, err)
	// Duplicate within one project is rejected.
	_, err = q.CreateProjectEnvironment(ctx, company.ID, project.ID, "production")
	require.Error(t, err)

	// Project envs never appear in the company-level listing (run injection).
	companyEnvs, err := q.ListEnvironments(ctx, company.ID)
	require.NoError(t, err)
	require.Len(t, companyEnvs, 1)
	require.Equal(t, "headcount1 cloud", companyEnvs[0].Name)

	// Entries: a variable and a secret, both sealed at rest.
	_, err = q.UpsertEnvironmentSecret(ctx, envA.ID, user.ID, "API_KEY", "prod-secret-value", db.EnvEntrySecret)
	require.NoError(t, err)
	_, err = q.UpsertEnvironmentSecret(ctx, envA.ID, user.ID, "NODE_ENV", "production", db.EnvEntryVariable)
	require.NoError(t, err)
	_, err = q.UpsertEnvironmentSecret(ctx, envA.ID, user.ID, "X", "v", "banana")
	require.Error(t, err, "invalid kind must be rejected")

	rows, err := q.ListEnvironmentSecrets(ctx, envA.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	kinds := map[string]string{}
	for _, r := range rows {
		kinds[r.Name] = r.Kind
		var raw string
		require.NoError(t, database.Raw("SELECT value FROM environment_secrets WHERE id = ?", r.ID).Scan(&raw).Error)
		require.True(t, secrets.IsSealed(raw), "%s must be sealed at rest regardless of kind", r.Name)
	}
	require.Equal(t, db.EnvEntrySecret, kinds["API_KEY"])
	require.Equal(t, db.EnvEntryVariable, kinds["NODE_ENV"])

	// Connector: token sealed at rest, write-only; sync status updatable.
	conn, err := q.CreateEnvironmentConnector(ctx, db.EnvironmentConnector{
		EnvironmentID:     envA.ID,
		Provider:          db.ConnectorVercel,
		TargetProject:     "prj_123",
		TargetEnvironment: "production",
	}, user.ID, "vercel-token-abc123456")
	require.NoError(t, err)
	require.True(t, conn.HasToken)

	var rawTok string
	require.NoError(t, database.Raw("SELECT api_token FROM environment_connectors WHERE id = ?", conn.ID).Scan(&rawTok).Error)
	require.True(t, secrets.IsSealed(rawTok))
	require.NotContains(t, rawTok, "vercel-token")

	_, err = q.CreateEnvironmentConnector(ctx, db.EnvironmentConnector{
		EnvironmentID: envA.ID, Provider: "netlify", TargetProject: "x", TargetEnvironment: "y",
	}, user.ID, "tok-123456789")
	require.Error(t, err, "unknown provider must be rejected")

	require.NoError(t, q.SetConnectorSyncResult(ctx, conn.ID, nil))
	got, err := q.GetEnvironmentConnector(ctx, conn.ID)
	require.NoError(t, err)
	require.Equal(t, "ok", got.LastSyncStatus)
	require.NotNil(t, got.LastSyncAt)

	require.NoError(t, q.SetConnectorSyncResult(ctx, conn.ID, fmt.Errorf("boom")))
	got, _ = q.GetEnvironmentConnector(ctx, conn.ID)
	require.Equal(t, "error", got.LastSyncStatus)
	require.Equal(t, "boom", got.LastSyncError)

	// Deleting the environment removes its connectors and entries.
	require.NoError(t, q.DeleteEnvironment(ctx, envA.ID))
	var n int64
	require.NoError(t, database.Model(&db.EnvironmentConnector{}).Where("environment_id = ?", envA.ID).Count(&n).Error)
	require.Zero(t, n)
}

func TestEnvironmentSecretSealedAtRest(t *testing.T) {
	q, database, company, user := setupEnvTest(t)
	ctx := context.Background()

	env, err := q.GetDefaultEnvironment(ctx, company.ID)
	require.NoError(t, err)

	const value = "env-secret-plaintext-value-42"
	row, err := q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "API_KEY", value, "")
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
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "API_KEY", "rotated-value", "")
	require.NoError(t, err)
	rows, _ = q.ListEnvironmentSecrets(ctx, env.ID)
	require.Len(t, rows, 1)
	plain, _ = secrets.Default().Decrypt(rows[0].ValueEncrypted)
	require.Equal(t, "rotated-value", plain)

	// Locked vault → write fails with ErrLocked (never silently unsealed).
	secrets.Default().LockUser(user.ID)
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "OTHER", "v", "")
	require.ErrorIs(t, err, secrets.ErrLocked)

	// Invalid env var names are rejected.
	secretsDekRelock(t, user.ID)
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "BAD NAME", "v", "")
	require.Error(t, err)
	_, err = q.UpsertEnvironmentSecret(ctx, env.ID, user.ID, "1LEADING", "v", "")
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

	_, err = q.UpsertEnvironmentSecret(ctx, def.ID, user.ID, "DEFAULT_ONLY", "dv", "")
	require.NoError(t, err)
	_, err = q.UpsertEnvironmentSecret(ctx, staging.ID, user.ID, "STAGING_ONLY", "sv", "")
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
