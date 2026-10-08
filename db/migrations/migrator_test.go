package migrations

import (
	"context"
	"database/sql"
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
			require.Len(t, manifest.Migrations, 71)
			for _, migration := range manifest.Migrations {
				require.NotEmpty(t, migration.UpSQL, migration.Version)
				require.NotEmpty(t, migration.DownSQL, "missing down migration for %s", migration.Version)
			}
			for _, version := range []string{"20260816000057", "20260816000058", "20260816000059", "20261007000001", "20261007000005", "20261007000006", "20261007000007"} {
				migration, ok := manifest.Entry(version)
				require.True(t, ok)
				require.False(t, migration.Reversible, "data-loss migration %s must require operator recovery", version)
			}
			for _, version := range []string{"20260816000056", "20260816000060", "20260816000061", "20261007000002", "20261007000003", "20261007000004", "20261008000001"} {
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

func TestPlanReconciliationAcceptsLegacyBaselineMarker(t *testing.T) {
	candidate := Manifest{Dialect: "postgres", Migrations: []Migration{
		{Version: "1", AtlasHash: "h1"},
		{Version: "2", AtlasHash: "h2"},
		{Version: "3", AtlasHash: "h3"},
		{Version: "4", AtlasHash: "h4"},
		{Version: "5", AtlasHash: "h5"},
	}}
	applied := []AppliedRevision{
		{Version: "3", Applied: 0, Total: 0},
		{Version: "4", Hash: "h4", Applied: 1, Total: 1},
	}

	plan, err := PlanReconciliation(applied, candidate, Manifest{})
	require.NoError(t, err)
	require.Equal(t, "4", plan.CommonVersion)
	require.Empty(t, plan.Rollback)
	require.Equal(t, []string{"5"}, migrationVersions(plan.Apply))
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
	require.Equal(t, 41, tables)

	var revisions int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM atlas_schema_revisions`).Scan(&revisions))
	require.Equal(t, 71, revisions)
	for _, column := range []string{"mode", "subagents", "model", "provider_id", "model_group_id", "chat_type", "permissions", "can_use_workers"} {
		var present int
		require.NoError(t, database.QueryRow(`SELECT count(*) FROM pragma_table_info('agents') WHERE name = ?`, column).Scan(&present))
		require.Zero(t, present, "legacy agent column %s should be removed", column)
	}
	// task_type was dropped as a legacy field and later reintroduced as the
	// workflow type; the legacy domain must not have survived.
	var legacyTypes int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM tasks WHERE task_type IN ('plan and implement', 'implement')`).Scan(&legacyTypes))
	require.Zero(t, legacyTypes)
	_, err = database.Exec(`INSERT INTO companies (name, short_name) VALUES ('c', 'c')`)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO sprints (company_id, name) VALUES (1, 's')`)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO tasks (company_id, sprint_id, title, task_type) VALUES (1, 1, 't', 'plan and implement')`)
	require.Error(t, err, "the legacy task_type domain must be rejected")
	_, err = database.Exec(`INSERT INTO tasks (company_id, sprint_id, title, task_type) VALUES (1, 1, 't', 'coding')`)
	require.NoError(t, err)

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
	require.Equal(t, 71, revisions)

	_ = database.Close()
}

// roundTripDB describes one dialect's database for the workflow round-trip
// test: where it is, which schema the migrations run in, and how to ask it
// whether a table or column exists.
type roundTripDB struct {
	database *sql.DB
	dialect  string
	schema   string
}

func (r roundTripDB) tableExists(t *testing.T, table string) bool {
	t.Helper()
	var present int
	if r.dialect == "sqlite" {
		require.NoError(t, r.database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&present))
	} else {
		require.NoError(t, r.database.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2`, r.schema, table).Scan(&present))
	}
	return present > 0
}

func (r roundTripDB) columnExists(t *testing.T, table, column string) bool {
	t.Helper()
	var present int
	if r.dialect == "sqlite" {
		require.NoError(t, r.database.QueryRow(`SELECT count(*) FROM pragma_table_info('`+table+`') WHERE name = ?`, column).Scan(&present))
	} else {
		require.NoError(t, r.database.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`, r.schema, table, column).Scan(&present))
	}
	return present > 0
}

// runWorkflowMigrationsRoundTrip takes the workflow migrations down and back
// up over existing rows: the down files must leave the pre-workflow schema
// behind, and re-applying must backfill each task's tree position.
func runWorkflowMigrationsRoundTrip(t *testing.T, r roundTripDB) {
	ctx := context.Background()
	database := r.database
	require.NoError(t, ApplyWithSchema(ctx, database, r.dialect, "test", r.schema))

	exec := func(statement string) {
		t.Helper()
		_, execErr := database.Exec(statement)
		require.NoError(t, execErr, statement)
	}
	count := func(query string) int {
		t.Helper()
		var n int
		require.NoError(t, database.QueryRow(query).Scan(&n), query)
		return n
	}
	exec(`INSERT INTO users (id, email) VALUES (1, 'owner@example.com')`)
	exec(`INSERT INTO companies (id, name, short_name) VALUES (1, 'c', 'c')`)
	exec(`INSERT INTO sprints (id, company_id, name) VALUES (1, 1, 's')`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title) VALUES (1, 1, 1, 'root')`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, parent_id) VALUES (2, 1, 1, 'child', 1)`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, parent_id) VALUES (3, 1, 1, 'grandchild', 2)`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title) VALUES (4, 1, 1, 'other root')`)
	exec(`INSERT INTO default_model_settings (purpose, user_id) VALUES ('smart', 1)`)
	exec(`INSERT INTO default_model_settings (purpose, user_id) VALUES ('commit_messages', 1)`)

	manifest, err := BuildManifestForSchema(r.dialect, r.schema)
	require.NoError(t, err)
	var rollback []Migration
	for i := len(manifest.Migrations) - 1; i >= 0; i-- {
		migration := manifest.Migrations[i]
		if migration.Version < "20261007000001" {
			break
		}
		// Some of these are flagged irreversible because they lose data on a
		// populated database; an operator may still run them, which is what
		// this exercises.
		migration.Reversible = true
		rollback = append(rollback, migration)
	}
	require.Len(t, rollback, 8)
	require.NoError(t, ApplyDownWithSchema(ctx, database, r.dialect, rollback, r.schema))

	require.Equal(t, 63, count(`SELECT count(*) FROM atlas_schema_revisions`))
	for _, table := range []string{"task_steps", "decisions", "llm_calls"} {
		require.False(t, r.tableExists(t, table), "%s should be dropped", table)
	}
	for _, column := range []string{"task_type", "mode", "phase", "waiting_on", "lease_owner", "root_task_id", "provider_id", "result_reason", "workspace_owner_task_id"} {
		require.False(t, r.columnExists(t, "tasks", column), "tasks.%s should be dropped", column)
	}
	require.False(t, r.columnExists(t, "comments", "reply_to_id"))
	require.False(t, r.columnExists(t, "runs", "report"))
	require.False(t, r.columnExists(t, "runs", "attempt"))
	_, err = database.Exec(`UPDATE tasks SET status = 'failed' WHERE id = 4`)
	require.Error(t, err, "the pre-workflow status domain must be restored")
	require.Equal(t, 1, count(`SELECT count(*) FROM default_model_settings`), "tier purposes are removed, others kept")
	_, err = database.Exec(`INSERT INTO default_model_settings (purpose, user_id) VALUES ('cheap', 1)`)
	require.Error(t, err, "the pre-workflow purpose domain must be restored")

	require.NoError(t, ApplyWithSchema(ctx, database, r.dialect, "test", r.schema))
	require.Equal(t, 71, count(`SELECT count(*) FROM atlas_schema_revisions`))
	rows, err := database.Query(`SELECT id, root_task_id, depth, task_type, mode FROM tasks ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	type position struct {
		id, root, depth int
		taskType, mode  string
	}
	var got []position
	for rows.Next() {
		var p position
		require.NoError(t, rows.Scan(&p.id, &p.root, &p.depth, &p.taskType, &p.mode))
		got = append(got, p)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []position{
		{1, 1, 0, "general", "managed"},
		{2, 1, 1, "general", "managed"},
		{3, 1, 2, "general", "managed"},
		{4, 4, 0, "general", "managed"},
	}, got)
	exec(`UPDATE tasks SET status = 'failed' WHERE id = 4`)
	exec(`INSERT INTO default_model_settings (purpose, user_id) VALUES ('classifier', 1)`)
	_, err = database.Exec(`UPDATE tasks SET waiting_on = 'nonsense' WHERE id = 1`)
	require.Error(t, err)
	_, err = database.Exec(`INSERT INTO task_steps (task_id, kind) VALUES (1, 'nonsense')`)
	require.Error(t, err)
	exec(`INSERT INTO task_steps (task_id, root_task_id, kind) VALUES (1, 1, 'workflow_started')`)
	exec(`INSERT INTO decisions (task_id, root_task_id, kind, title) VALUES (1, 1, 'dead_end', 'tried x')`)
	exec(`INSERT INTO llm_calls (company_id, task_id, tier) VALUES (1, 1, 'smart')`)
	_, err = database.Exec(`INSERT INTO llm_calls (company_id, tier) VALUES (1, 'nonsense')`)
	require.Error(t, err)
}

// runCleanSlateDataMigration applies the two destructive cutover migrations
// to a database populated the way the old engine left it, and checks what
// must survive: the user's tasks with their assignees, their artifacts, their
// own agents' prompts and skills, and the models they had configured.
func runCleanSlateDataMigration(t *testing.T, r roundTripDB) {
	ctx := context.Background()
	database := r.database
	require.NoError(t, ApplyWithSchema(ctx, database, r.dialect, "test", r.schema))

	exec := func(statement string) {
		t.Helper()
		_, execErr := database.Exec(statement)
		require.NoError(t, execErr, statement)
	}
	count := func(query string) int {
		t.Helper()
		var n int
		require.NoError(t, database.QueryRow(query).Scan(&n), query)
		return n
	}
	text := func(query string) string {
		t.Helper()
		var value sql.NullString
		require.NoError(t, database.QueryRow(query).Scan(&value), query)
		return value.String
	}

	// Back to the schema the old engine ran on.
	manifest, err := BuildManifestForSchema(r.dialect, r.schema)
	require.NoError(t, err)
	var rollback []Migration
	for i := len(manifest.Migrations) - 1; i >= 0 && manifest.Migrations[i].Version >= "20261007000006"; i-- {
		migration := manifest.Migrations[i]
		migration.Reversible = true
		rollback = append(rollback, migration)
	}
	require.Len(t, rollback, 3)
	require.NoError(t, ApplyDownWithSchema(ctx, database, r.dialect, rollback, r.schema))

	exec(`INSERT INTO users (id, email) VALUES (1, 'owner@example.com')`)
	exec(`INSERT INTO companies (id, name, short_name, user_id) VALUES (1, 'c', 'c', 1)`)
	exec(`INSERT INTO sprints (id, company_id, name) VALUES (1, 1, 's')`)
	exec(`INSERT INTO llm_providers (id, name, base_url, api_key, user_id) VALUES (1, 'p', 'https://example.test', '', 1)`)
	exec(`INSERT INTO skills (id, company_id, name, local_path) VALUES (1, 1, 'deploys', '/tmp/skills/deploys')`)
	exec(`INSERT INTO agents (id, company_id, name, role_key, builtin, system_prompt, provider_id, model, permissions)
	      VALUES (1, 1, 'CEO', 'CEO', true, 'You delegate to the orchestrator and its workers.', 1, 'strong-model', '{"bash":"deny"}')`)
	exec(`INSERT INTO agents (id, company_id, name, role_key, builtin, system_prompt, provider_id, model)
	      VALUES (2, 1, 'Coder', 'Coder', true, 'Use the worker tools.', 1, 'coder-model')`)
	exec(`INSERT INTO agents (id, company_id, name, role_key, builtin, system_prompt)
	      VALUES (3, 1, 'Boat expert', 'boat-expert', false, 'You know everything about boats.')`)
	exec(`INSERT INTO agent_skills (agent_id, skill_id) VALUES (2, 1)`)
	exec(`INSERT INTO agent_skills (agent_id, skill_id) VALUES (3, 1)`)
	exec(`INSERT INTO agent_mcp_servers (agent_id, mcp_server_id) VALUES (2, 1)`)

	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, status, agent_id) VALUES (1, 1, 1, 'under way', 'in-progress', 2)`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, status, agent_id) VALUES (2, 1, 1, 'being refined', 'refinement', 1)`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, status, agent_id) VALUES (3, 1, 1, 'finished', 'done', 3)`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, status, agent_id) VALUES (4, 1, 1, 'queued', 'to-do', 3)`)
	exec(`INSERT INTO tasks (id, company_id, sprint_id, title, status, agent_id, parent_id) VALUES (5, 1, 1, 'delegated', 'blocked', 2, 1)`)
	exec(`INSERT INTO runs (id, task_id, agent_id, status, kind) VALUES (1, 1, 1, 'running', 'task_orchestrator')`)
	exec(`INSERT INTO runs (id, task_id, agent_id, status, kind, parent_run_id, root_run_id) VALUES (2, 5, 2, 'waiting', 'agent_session', 1, 1)`)
	exec(`UPDATE tasks SET run_id = 1, orchestrator_run_id = 1 WHERE id = 1`)
	exec(`UPDATE tasks SET run_id = 2 WHERE id = 5`)
	exec(`INSERT INTO comments (id, task_id, author_type, content, run_id) VALUES (1, 1, 'agent', 'progress', 2)`)
	exec(`INSERT INTO artifacts (id, company_id, task_id, run_id, filename, file_path) VALUES (1, 1, 1, 2, 'spec.md', '/tmp/spec.md')`)
	exec(`INSERT INTO run_events (task_id, run_id, event_type) VALUES (1, 1, 'run_status')`)
	exec(`INSERT INTO run_status_reports (run_id, status) VALUES (2, 'working')`)
	exec(`INSERT INTO proxy_request_logs (agent_id, provider_id, model) VALUES (1, 1, 'strong-model')`)
	exec(`INSERT INTO default_model_settings (purpose, user_id, provider_id, model) VALUES ('helper_worker', 1, 1, 'helper-model')`)
	exec(`INSERT INTO default_model_settings (purpose, user_id, provider_id, model) VALUES ('task_orchestrator', 1, 1, 'orchestrator-model')`)
	exec(`INSERT INTO default_model_settings (purpose, user_id, provider_id, model) VALUES ('commit_messages', 1, 1, 'commit-model')`)

	require.NoError(t, ApplyWithSchema(ctx, database, r.dialect, "test", r.schema))

	// The old engine's execution data is gone.
	require.Zero(t, count(`SELECT count(*) FROM runs`))
	for _, table := range []string{"run_events", "run_status_reports", "proxy_request_logs", "agent_mcp_servers", "agent_mcp_accounts", "agent_mcp_tool_filters"} {
		require.False(t, r.tableExists(t, table), "%s should be dropped", table)
	}
	for _, column := range []string{"kind", "parent_run_id", "root_run_id", "current_status"} {
		require.False(t, r.columnExists(t, "runs", column), "runs.%s should be dropped", column)
	}
	require.False(t, r.columnExists(t, "tasks", "orchestrator_run_id"))

	// Deliverables and the discussion outlive the runs that produced them.
	require.Equal(t, 1, count(`SELECT count(*) FROM artifacts WHERE filename = 'spec.md' AND run_id IS NULL`))
	require.Equal(t, 1, count(`SELECT count(*) FROM comments WHERE content = 'progress' AND run_id IS NULL`))

	// Every task is kept with its assignee; the ones that were mid-run wait in
	// the backlog, the rest stay where they were.
	rows, err := database.Query(`SELECT id, status, agent_id FROM tasks WHERE run_id IS NULL ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	type kept struct {
		id      int
		status  string
		agentID int
	}
	var tasks []kept
	for rows.Next() {
		var task kept
		require.NoError(t, rows.Scan(&task.id, &task.status, &task.agentID))
		tasks = append(tasks, task)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []kept{
		{1, "backlog", 2},
		{2, "backlog", 1},
		{3, "done", 3},
		{4, "to-do", 3},
		{5, "backlog", 2},
	}, tasks)
	_, err = database.Exec(`UPDATE tasks SET status = 'refinement' WHERE id = 2`)
	require.Error(t, err, "the refinement status is gone")

	// Agents are roles: the built-in prompts are reset, a user's own agent
	// keeps its prompt, and skills stay attached.
	require.Equal(t, 3, count(`SELECT count(*) FROM agents`))
	require.Equal(t, "You are the CEO agent.", text(`SELECT system_prompt FROM agents WHERE id = 1`))
	require.Equal(t, "You are the Coder agent.", text(`SELECT system_prompt FROM agents WHERE id = 2`))
	require.Equal(t, "You know everything about boats.", text(`SELECT system_prompt FROM agents WHERE id = 3`))
	require.Equal(t, 2, count(`SELECT count(*) FROM agent_skills`))
	require.Equal(t, 1, count(`SELECT count(*) FROM agent_skills WHERE agent_id = 2 AND skill_id = 1`))
	for _, column := range []string{"provider_id", "model_group_id", "model", "chat_type", "reasoning_level", "permissions", "allowed_mc_ps", "can_use_workers", "worker_permissions", "worker_allowed_mc_ps"} {
		require.False(t, r.columnExists(t, "agents", column), "agents.%s should be dropped", column)
	}

	// The configured models carry over to the tiers: cheap from the helper
	// worker, smart from the CEO agent.
	require.Equal(t, "helper-model", text(`SELECT model FROM default_model_settings WHERE user_id = 1 AND purpose = 'cheap'`))
	require.Equal(t, "strong-model", text(`SELECT model FROM default_model_settings WHERE user_id = 1 AND purpose = 'smart'`))
	require.Equal(t, 1, count(`SELECT count(*) FROM default_model_settings WHERE user_id = 1 AND purpose = 'smart' AND provider_id = 1`))
	require.Equal(t, "commit-model", text(`SELECT model FROM default_model_settings WHERE user_id = 1 AND purpose = 'commit_messages'`))
	require.Zero(t, count(`SELECT count(*) FROM default_model_settings WHERE purpose IN ('helper_worker', 'task_orchestrator')`))

	// The new engine can work in what is left.
	exec(`INSERT INTO runs (task_id, agent_id, status) VALUES (1, 2, 'running')`)
	_, err = database.Exec(`INSERT INTO runs (task_id, agent_id, status) VALUES (1, 2, 'waiting')`)
	require.Error(t, err, "the old run statuses are gone")
}

func TestApplyCleanSlateDataMigrationSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanslate.db")
	gormDB, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)
	defer database.Close()
	runCleanSlateDataMigration(t, roundTripDB{database: database, dialect: "sqlite"})
}

func TestApplyCleanSlateDataMigrationPostgres(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	const schema = "headcount1_cleanslate_test"
	dsn, err := PostgresSearchPath(url, schema)
	require.NoError(t, err)
	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)
	defer database.Close()
	_, err = database.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
	require.NoError(t, err)
	_, err = database.Exec(`CREATE SCHEMA "` + schema + `"`)
	require.NoError(t, err)
	defer database.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
	runCleanSlateDataMigration(t, roundTripDB{database: database, dialect: "postgres", schema: schema})
}

func TestApplyWorkflowMigrationsRoundTripSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.db")
	gormDB, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)
	defer database.Close()
	runWorkflowMigrationsRoundTrip(t, roundTripDB{database: database, dialect: "sqlite"})
}

// Named for the Postgres CI job, which runs ./db/migrations tests matching
// ^TestApply.
func TestApplyWorkflowMigrationsRoundTripPostgres(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	// A schema of its own keeps this destructive round trip away from the
	// public schema the other Postgres tests share.
	const schema = "headcount1_roundtrip_test"
	dsn, err := PostgresSearchPath(url, schema)
	require.NoError(t, err)
	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	database, err := gormDB.DB()
	require.NoError(t, err)
	defer database.Close()
	_, err = database.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
	require.NoError(t, err)
	_, err = database.Exec(`CREATE SCHEMA "` + schema + `"`)
	require.NoError(t, err)
	defer database.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
	runWorkflowMigrationsRoundTrip(t, roundTripDB{database: database, dialect: "postgres", schema: schema})
}
