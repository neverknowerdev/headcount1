package db_test

// This is a full-surface integration test that runs the real AutoMigrate list
// (mirroring main.go) and exercises the query layer against a REAL PostgreSQL
// database. It is skipped unless TEST_POSTGRES_URL is set, e.g.:
//
//	TEST_POSTGRES_URL="postgres://postgres@127.0.0.1:5433/orchestrator?sslmode=disable" \
//	    go test ./db/ -run TestPostgres -v
//
// The SQLite unit tests cover behavior; this test's job is to catch
// Postgres-specific gaps (dialect SQL, ON CONFLICT, sequence handling, type
// coercion) that in-memory SQLite would never surface.

import (
	"context"
	"os"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// allModels mirrors the AutoMigrate list in main.go exactly.
func allModels() []any {
	return []any{
		&db.User{},
		&db.WebAuthnCredential{},
		&db.WebAuthnSession{},
		&db.Team{},
		&db.TeamMember{},
		&db.TeamInvite{},
		&db.Session{},
		&db.RefreshToken{},
		&db.UserGitCredential{},
		&db.PasswordResetToken{},
		&db.Company{},
		&db.Project{},
		&db.GitHubOAuthState{},
		&db.GitHubConnection{},
		&db.GitHubIdentity{},
		&db.GitHubWebhookDelivery{},
		&db.GitHubWebhookTarget{},
		&db.Sprint{},
		&db.LLMProvider{},
		&db.ModelGroup{},
		&db.ModelGroupMember{},
		&db.ModelRequestStat{},
		&db.DefaultModelSetting{},
		&db.ProviderPreset{},
		&db.Agent{},
		&db.Skill{},
		&db.Task{},
		&db.TaskRelation{},
		&db.Comment{},
		&db.Attachment{},
		&db.Run{},
		&db.RunStatusReport{},
		&db.Artifact{},
		&db.ActivityLog{},
		&db.ProxyRequestLog{},
		&db.MCPServer{},
		&db.MCPAccount{},
		&db.AgentMCPServer{},
		&db.AgentMCPAccount{},
		&db.MCPToolStat{},
		&db.AgentMCPToolFilter{},
	}
}

func openPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	return database
}

// dropEverything drops all tables so each run starts from a clean schema. It
// also drops join tables that AutoMigrate creates implicitly (agent_skills).
func dropEverything(t *testing.T, database *gorm.DB) {
	t.Helper()
	// DROP SCHEMA is the cleanest full reset for a dedicated test database.
	require.NoError(t, database.Exec("DROP SCHEMA public CASCADE").Error)
	require.NoError(t, database.Exec("CREATE SCHEMA public").Error)
}

func TestPostgresAutoMigrate(t *testing.T) {
	database := openPostgres(t)
	dropEverything(t, database)
	require.NoError(t, database.AutoMigrate(allModels()...), "AutoMigrate against Postgres")

	// AutoMigrate must be idempotent — a second run (upgrade path) must not error.
	require.NoError(t, database.AutoMigrate(allModels()...), "second AutoMigrate must be idempotent")
	var recoveryType string
	require.NoError(t, database.Raw(`SELECT udt_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'runs' AND column_name = 'recovery'`).Scan(&recoveryType).Error)
	require.Equal(t, "jsonb", recoveryType)
}

func TestPostgresQuerySurface(t *testing.T) {
	database := openPostgres(t)
	dropEverything(t, database)
	require.NoError(t, database.AutoMigrate(allModels()...))
	q := db.New(database)
	ctx := context.Background()

	// ── Seeding paths (run on every startup) ──────────────────────────────
	require.NoError(t, q.EnsureBuiltinMCPServers(ctx), "EnsureBuiltinMCPServers")
	require.NoError(t, q.EnsureProviderPresets(ctx), "EnsureProviderPresets")
	// Idempotency (re-run on next boot).
	require.NoError(t, q.EnsureBuiltinMCPServers(ctx), "EnsureBuiltinMCPServers (2nd)")
	require.NoError(t, q.EnsureProviderPresets(ctx), "EnsureProviderPresets (2nd)")

	// ── User + vault (sealed columns) ─────────────────────────────────────
	user, err := q.CreateUser(ctx, "pg@example.com")
	require.NoError(t, err)
	dek, _ := secrets.NewUserDEK()
	secrets.Default().UnlockUser(user.ID, dek, time.Minute)
	defer secrets.Default().LockUser(user.ID)

	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, user.ID), "EnsureBuiltinLLMProvidersForUser")
	require.NoError(t, q.EnsureBuiltinLLMProvidersForUser(ctx, user.ID), "EnsureBuiltinLLMProvidersForUser (2nd)")

	// Sealed provider key round-trip.
	sealed, err := secrets.Default().EncryptForUser(user.ID, "sk-secret")
	require.NoError(t, err)
	prov, err := q.CreateLLMProvider(ctx, db.LLMProvider{
		Name: "custom", BaseUrl: "https://api.example.com",
		ApiKeyEncrypted: sealed, UserID: &user.ID,
	})
	require.NoError(t, err)
	require.True(t, prov.HasApiKey)

	// Git credential upsert uses clause.OnConflict — a Postgres-critical path.
	require.NoError(t, q.UpsertUserSSHKey(ctx, user.ID, "-----BEGIN KEY-----\nabc\n-----END KEY-----"), "UpsertUserSSHKey insert")
	require.NoError(t, q.UpsertUserSSHKey(ctx, user.ID, "-----BEGIN KEY-----\nxyz\n-----END KEY-----"), "UpsertUserSSHKey conflict-update")

	// ── Company → project → agent → task → run tree ───────────────────────
	company, err := q.CreateCompany(ctx, "Acme")
	require.NoError(t, err)
	proj, err := q.CreateProject(ctx, db.Project{CompanyID: company.ID, Name: "web"})
	require.NoError(t, err)
	sprint, err := q.CreateSprint(ctx, db.Sprint{CompanyID: company.ID, Name: "S1"})
	require.NoError(t, err)
	agent, err := q.CreateAgent(ctx, db.Agent{CompanyID: company.ID, Name: "CEO", SystemPrompt: "be a ceo"})
	require.NoError(t, err)
	task, err := q.CreateTask(ctx, db.Task{
		CompanyID: company.ID, ProjectID: &proj.ID, SprintID: sprint.ID,
		AgentID: &agent.ID, Title: "do it",
	})
	require.NoError(t, err)
	run, err := q.CreateRun(ctx, db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running", Name: "T-1-CEO"})
	require.NoError(t, err)

	// ── MCPToolStat upsert: INSERT ... ON CONFLICT DO UPDATE (raw SQL) ─────
	var srv db.MCPServer
	require.NoError(t, database.WithContext(ctx).Where("name = ?", "github").First(&srv).Error, "builtin github server seeded")
	require.NoError(t, q.IncrementMCPToolCallCount(ctx, srv.ID, "search_code"))
	require.NoError(t, q.IncrementMCPToolCallCount(ctx, srv.ID, "search_code"))
	counts, err := q.GetMCPToolCallCounts(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), counts["search_code"], "ON CONFLICT increment must accumulate")

	// ── Codegraph assignments + tool filters (raw INSERT with bool=false) ──
	require.NoError(t, q.SetAgentMCPToolFilters(ctx, agent.ID, []db.AgentMCPToolFilter{
		{AgentID: agent.ID, MCPServerID: srv.ID, ToolName: "search_code", Enabled: false},
	}), "SetAgentMCPToolFilters")
	filters, err := q.GetAgentMCPToolFilters(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, false, filters[srv.ID]["search_code"], "Enabled=false must persist")

	// ── FK-constraint rebuild migration (must no-op on Postgres) ──────────
	require.NoError(t, q.MigrateAddProjectFKToMCPServers(ctx), "MigrateAddProjectFKToMCPServers")

	// ── Run key uniqueness query (LIKE with a suffix wildcard) ────────────
	require.NoError(t, q.UpdateRunCurrentStatus(ctx, run.ID, "working"))

	_ = user
}
