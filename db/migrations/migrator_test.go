package migrations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestApplySQLiteEmbeddedMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	gormDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)

	require.NoError(t, Apply(context.Background(), database, "sqlite", "test"))
	require.NoError(t, Apply(context.Background(), database, "sqlite", "test"))

	var tables int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables))
	require.Equal(t, 44, tables)

	var revisions int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM atlas_schema_revisions`).Scan(&revisions))
	require.Equal(t, 54, revisions)

	// Simulate a legacy pre-Atlas database: retain all application tables but
	// remove Atlas's revision history. The runner must baseline the initial
	// schema and still execute the follow-up enum migrations.
	for _, statement := range []string{
		"ALTER TABLE team_members DROP COLUMN __enum_guard_role",
		"ALTER TABLE team_invites DROP COLUMN __enum_guard_role",
		"ALTER TABLE agents DROP COLUMN __enum_guard_reasoning_level",
		"ALTER TABLE agents DROP COLUMN __enum_guard_chat_type",
		"ALTER TABLE tasks DROP COLUMN __enum_guard_status",
		"ALTER TABLE tasks DROP COLUMN __enum_guard_task_type",
		"ALTER TABLE task_relations DROP COLUMN __enum_guard_kind",
		"ALTER TABLE runs DROP COLUMN __enum_guard_status",
		"ALTER TABLE run_events DROP COLUMN __enum_guard_event_type",
		"ALTER TABLE default_model_settings DROP COLUMN __enum_guard_purpose",
		"ALTER TABLE git_hub_webhook_deliveries DROP COLUMN __enum_guard_status",
		"ALTER TABLE git_hub_webhook_targets DROP COLUMN __enum_guard_wake_status",
	} {
		_, err = database.Exec(statement)
		require.NoError(t, err)
	}
	_, err = database.Exec(`DROP TABLE atlas_schema_revisions`)
	require.NoError(t, err)
	require.NoError(t, Apply(context.Background(), database, "sqlite", "test"))
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'atlas_schema_revisions'`).Scan(&revisions))
	require.Equal(t, 1, revisions)
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM atlas_schema_revisions`).Scan(&revisions))
	require.Equal(t, 12, revisions)

	// The follow-up status migration must allow legacy refinement rows.
	for _, statement := range []string{
		"INSERT INTO companies (id, name, short_name) VALUES (1, 'Migration company', 'migration')",
		"INSERT INTO sprints (id, company_id, name) VALUES (1, 1, 'Migration sprint')",
		"INSERT INTO tasks (company_id, sprint_id, title, status) VALUES (1, 1, 'Legacy refinement task', 'refinement')",
	} {
		_, err = database.Exec(statement)
		require.NoError(t, err)
	}

	_ = database.Close()
}

func TestApplyPostgresEmbeddedMigrations(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	gormDB, err := gorm.Open(postgres.Open(url), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)

	require.NoError(t, Apply(context.Background(), database, "postgres", "test"))
	require.NoError(t, Apply(context.Background(), database, "postgres", "test"))

	var revisions int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM public.atlas_schema_revisions`).Scan(&revisions))
	require.Equal(t, 54, revisions)

	_ = database.Close()
}
