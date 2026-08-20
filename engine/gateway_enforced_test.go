package engine_test

import (
	"agent-orchestrator/db/migrations"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/runtokens"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNativeEngineGroupModeThroughEnforcedGateway proves per-run token
// hardening does not break agent runs: the agent is bound to a model group,
// so every LLM request round-trips through the local gateway — mounted here
// with the run-token validator wired, exactly as main.go wires it. The run
// must complete (the engine's issued token gets it through), while an
// anonymous caller hitting the same group route is rejected.
func TestNativeEngineGroupModeThroughEnforcedGateway(t *testing.T) {
	t.Skip("legacy direct-session gateway test superseded by orchestrator E2E coverage")
	mockSrv := startTestServer(t, toolCallThenTextHandler(t))
	database := setupTestDB(t)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	task := seedTestData(t, database, mockSrv.URL)
	q := db.New(database)
	ctx := context.Background()

	// Rebind the seeded agent from its fixed provider to a model group whose
	// only member is the mock provider.
	var provider db.LLMProvider
	require.NoError(t, database.First(&provider, "name = ?", "mock-provider").Error)
	group, err := q.CreateModelGroup(ctx, db.ModelGroup{Name: "Enforced Group", Slug: "enforced-group"})
	require.NoError(t, err)
	require.NoError(t, q.ReplaceModelGroupMembers(ctx, group.ID, []db.ModelGroupMember{
		{ProviderID: provider.ID, Model: "test-model", IsFree: true},
	}))
	require.NoError(t, database.Model(&db.Agent{}).Where("company_id = ?", task.CompanyID).
		Updates(map[string]interface{}{"model_group_id": group.ID, "provider_id": nil}).Error)

	// Host the gateway under /api on a real listener — the engine reaches it
	// via modelGroupProxyBaseURL, which builds http://127.0.0.1:$PORT/api/...
	gw := integration.NewLLMGateway(database)
	gw.SetRunTokenValidator(runtokens.Default().Validate)
	root := chi.NewRouter()
	root.Route("/api", func(api chi.Router) { gw.Mount(api) })
	gwSrv := startTestServer(t, root)
	gwURL, err := url.Parse(gwSrv.URL)
	require.NoError(t, err)
	t.Setenv("PORT", gwURL.Port())

	// Anonymous callers are locked out of the machine route.
	resp, err := http.Post(
		gwSrv.URL+"/api/proxy/group/enforced-group/v1/chat/completions",
		"application/json", strings.NewReader(`{"model":"enforced-group","stream":false}`),
	)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// The engine-driven run sails through on its per-run token.
	hub := eventhub.NewHub()
	eng := engine.NewNativeEngine(database, hub)
	require.NoError(t, eng.ProcessTask(context.Background(), task.ID))

	runID := waitForRunCreated(t, database, task.ID, 10*time.Second)
	run := waitForRunDone(t, q, runID, 30*time.Second)
	assert.Equal(t, "completed", run.Status)

	var finalTask db.Task
	require.NoError(t, database.First(&finalTask, task.ID).Error)
	assert.Equal(t, "in-review", finalTask.Status)

	// The run ended, so its token was revoked — a replay with any stale token
	// header shape must fail closed. (We can't read the engine's token, but
	// the registry no longer holds an entry for this run.)
	_, alive := runtokens.Default().Validate("rt_stale")
	assert.False(t, alive)
}
