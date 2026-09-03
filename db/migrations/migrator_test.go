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

func TestMigrationManifestsAuditEveryDownPair(t *testing.T) {
	for _, dialect := range []string{"postgres", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			manifest, err := BuildManifest(dialect)
			require.NoError(t, err)
			require.Len(t, manifest.Migrations, 61)
			for _, migration := range manifest.Migrations {
				require.NotEmpty(t, migration.UpSQL, migration.Version)
				require.NotEmpty(t, migration.DownSQL, "missing down migration for %s", migration.Version)
			}
			for _, version := range []string{"20260816000057", "20260816000058", "20260816000059"} {
				migration, ok := manifest.Entry(version)
				require.True(t, ok)
				require.False(t, migration.Reversible, "data-loss migration %s must require operator recovery", version)
			}
			for _, version := range []string{"20260816000056", "20260816000060", "20260816000061"} {
				migration, ok := manifest.Entry(version)
				require.True(t, ok)
				require.True(t, migration.Reversible, "schema migration %s should be automatically reversible", version)
			}
		})
	}
}

func TestPostgresShadowManifestRewritesPublicReferencesAndHashesRenderedSQL(t *testing.T) {
	manifest, err := BuildManifestForSchema("postgres", "headcount1_deploy_test")
	require.NoError(t, err)
	require.Equal(t, "headcount1_deploy_test", manifest.Schema)
	entry, ok := manifest.Entry("20260816000001")
	require.True(t, ok)
	require.Contains(t, entry.UpSQL, `"headcount1_deploy_test"."users"`)
	require.NotContains(t, entry.UpSQL, `"public"."users"`)
	require.NotEqual(t, digest([]byte(entry.UpSQL)), entry.AtlasHash)
}

func TestPostgresShadowSchemaNameValidation(t *testing.T) {
	_, err := BuildManifestForSchema("postgres", "bad-schema")
	require.Error(t, err)
	dsn, err := PostgresSearchPath("postgres://localhost/db?sslmode=disable", "headcount1_deploy_test")
	require.NoError(t, err)
	require.Contains(t, dsn, "search_path=headcount1_deploy_test,public")
	_, err = PostgresSearchPath("postgres://localhost/db", "bad-schema")
	require.Error(t, err)
}

func TestPlanReconciliationFindsCommonPrefix(t *testing.T) {
	previous := Manifest{Dialect: "sqlite", Migrations: []Migration{
		{Version: "1", AtlasHash: "h1", Reversible: true, DownSQL: "DROP TABLE a"},
		{Version: "2", AtlasHash: "h2", Reversible: true, DownSQL: "DROP TABLE b"},
		{Version: "3", AtlasHash: "h3", Reversible: true, DownSQL: "DROP TABLE c"},
	}}
	candidate := Manifest{Dialect: "sqlite", Migrations: []Migration{
		previous.Migrations[0],
		previous.Migrations[1],
		{Version: "4", AtlasHash: "h4", Reversible: true, DownSQL: "DROP TABLE d"},
	}}
	plan, err := PlanReconciliation([]AppliedRevision{{Version: "1", Hash: "h1", Applied: 1, Total: 1}, {Version: "2", Hash: "h2", Applied: 1, Total: 1}, {Version: "3", Hash: "h3", Applied: 1, Total: 1}}, candidate, previous)
	require.NoError(t, err)
	require.Equal(t, "2", plan.CommonVersion)
	require.Equal(t, []string{"3"}, migrationVersions(plan.Rollback))
	require.Equal(t, []string{"4"}, migrationVersions(plan.Apply))
}

func TestPlanReconciliationRejectsChangedHistoryAndIrreversibleRollback(t *testing.T) {
	candidate := Manifest{Dialect: "sqlite", Migrations: []Migration{{Version: "1", AtlasHash: "new", Reversible: true, DownSQL: "DROP TABLE a"}}}
	_, err := PlanReconciliation([]AppliedRevision{{Version: "1", Hash: "old", Applied: 1, Total: 1}}, candidate, Manifest{})
	var mismatch *HistoryMismatchError
	require.ErrorAs(t, err, &mismatch)

	previous := Manifest{Dialect: "sqlite", Migrations: []Migration{{Version: "1", AtlasHash: "old", Reversible: false, DownSQL: "-- irreversible"}}}
	candidate = Manifest{Dialect: "sqlite", Migrations: []Migration{{Version: "2", AtlasHash: "new", Reversible: true, DownSQL: "DROP TABLE b"}}}
	_, err = PlanReconciliation([]AppliedRevision{{Version: "1", Hash: "old", Applied: 1, Total: 1}}, candidate, previous)
	var irreversible *IrreversibleMigrationError
	require.ErrorAs(t, err, &irreversible)

	_, err = PlanReconciliation([]AppliedRevision{{Version: "1", Hash: "old", Applied: 1, Total: 2, Error: "bad statement"}}, candidate, previous)
	var partial *MigrationError
	require.ErrorAs(t, err, &partial)
	require.Contains(t, partial.Error(), "revision is partial")
}

func TestReconcileSQLitePersistsCandidateManifest(t *testing.T) {
	basePath := t.TempDir()
	gormDB, err := gorm.Open(sqlite.Open(filepath.Join(basePath, "test.db")), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)
	defer database.Close()

	candidate, err := BuildManifest("sqlite")
	require.NoError(t, err)
	require.NoError(t, Reconcile(context.Background(), database, "sqlite", "test", basePath, candidate))

	saved, err := LoadManifest(basePath, "sqlite")
	require.NoError(t, err)
	require.Equal(t, candidate.Dialect, saved.Dialect)
	require.Equal(t, candidate.Migrations[len(candidate.Migrations)-1].AtlasHash, saved.Migrations[len(saved.Migrations)-1].AtlasHash)
	require.Len(t, saved.Migrations, len(candidate.Migrations))
}

func TestApplyDownSQLiteIsTransactional(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open("file:down-transaction?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)
	defer database.Close()
	_, err = database.Exec(`CREATE TABLE atlas_schema_revisions (version TEXT PRIMARY KEY); CREATE TABLE sample (id INTEGER)`)
	require.NoError(t, err)
	migration := Migration{Version: "1", Reversible: true, DownSQL: "DROP TABLE sample;"}
	require.NoError(t, ApplyDown(context.Background(), database, "sqlite", []Migration{migration}))
	_, err = database.Exec(`SELECT 1 FROM sample`)
	require.Error(t, err)
	var revisions int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM atlas_schema_revisions`).Scan(&revisions))
	require.Zero(t, revisions)

	_, err = database.Exec(`CREATE TABLE sample (id INTEGER)`)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO atlas_schema_revisions(version) VALUES ('2')`)
	require.NoError(t, err)
	failing := Migration{Version: "2", Reversible: true, DownSQL: "DROP TABLE sample; THIS IS INVALID;"}
	require.Error(t, ApplyDown(context.Background(), database, "sqlite", []Migration{failing}))
	_, err = database.Exec(`SELECT 1 FROM sample`)
	require.NoError(t, err, "failed down migration must roll back its DDL")
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM atlas_schema_revisions WHERE version = '2'`).Scan(&revisions))
	require.Equal(t, 1, revisions)
}

func migrationVersions(migrations []Migration) []string {
	versions := make([]string, len(migrations))
	for i := range migrations {
		versions[i] = migrations[i].Version
	}
	return versions
}

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
	require.Equal(t, 62, revisions)
	for _, column := range []string{"mode", "subagents"} {
		var present int
		require.NoError(t, database.QueryRow(`SELECT count(*) FROM pragma_table_info('agents') WHERE name = ?`, column).Scan(&present))
		require.Zero(t, present, "legacy agent column %s should be removed", column)
	}
	for _, column := range []string{"task_type", "__enum_guard_task_type"} {
		var present int
		require.NoError(t, database.QueryRow(`SELECT count(*) FROM pragma_table_info('tasks') WHERE name = ?`, column).Scan(&present))
		require.Zero(t, present, "legacy task column %s should be removed", column)
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
	require.Equal(t, 62, revisions)

	_ = database.Close()
}
