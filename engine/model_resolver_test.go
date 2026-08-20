package engine

import (
	"context"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestParseSupportedModels(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"model-a", []string{"model-a"}},
		{"model-a,model-b,model-c", []string{"model-a", "model-b", "model-c"}},
		{" model-a , model-b ", []string{"model-a", "model-b"}},
		{",,model-a,,", []string{"model-a"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseSupportedModels(tt.input))
		})
	}
}

func provider(defaultModel, supportedModels string) db.LLMProvider {
	return db.LLMProvider{
		Name:            "test-provider",
		DefaultModel:    defaultModel,
		SupportedModels: supportedModels,
	}
}

func TestResolveModel_AgentModelWins(t *testing.T) {
	m, err := resolveModel(provider("default-m", "default-m,other-m"), "agent-m")
	require.NoError(t, err)
	assert.Equal(t, "agent-m", m)
}

func TestResolveModel_EmptyAgentModelUsesProviderDefault(t *testing.T) {
	m, err := resolveModel(provider("default-m", "default-m"), "")
	require.NoError(t, err)
	assert.Equal(t, "default-m", m)
}

func TestResolveModel_NoModelAnywhereErrors(t *testing.T) {
	_, err := resolveModel(provider("", ""), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default model")
}

func modelResolverTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "resolver.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	return database
}

func TestModelGroupPurposeResolutionUsesGatewayInsteadOfFirstMember(t *testing.T) {
	database := modelResolverTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	owner, err := q.CreateUser(ctx, "model-group-resolver@test.local")
	require.NoError(t, err)
	free := db.LLMProvider{Name: "Free", BaseUrl: "https://free.example/v1", DefaultModel: "free-model"}
	paid := db.LLMProvider{Name: "Paid", BaseUrl: "https://paid.example/v1", DefaultModel: "paid-model"}
	require.NoError(t, database.Create(&free).Error)
	require.NoError(t, database.Create(&paid).Error)
	group, err := q.CreateModelGroup(ctx, db.ModelGroup{Name: "DeepSeek", Slug: "deepseek"})
	require.NoError(t, err)
	require.NoError(t, q.ReplaceModelGroupMembers(ctx, group.ID, []db.ModelGroupMember{
		{ProviderID: free.ID, Model: "free-model", IsFree: true},
		{ProviderID: paid.ID, Model: "paid-model", IsFree: false},
	}))
	require.NoError(t, database.Create(&db.DefaultModelSetting{
		Purpose: db.PurposeTaskOrchestrator, UserID: &owner.ID, ModelGroupID: &group.ID,
	}).Error)

	engine := NewNativeEngine(database, nil)
	provider, model, err := engine.resolveRequiredPurposeModel(ctx, owner.ID, db.PurposeTaskOrchestrator)
	require.NoError(t, err)
	require.Equal(t, "deepseek", model)
	require.Equal(t, modelGroupProxyBaseURL("deepseek"), provider.BaseUrl)
	require.True(t, isModelGroupProxyBaseURL(provider.BaseUrl))
	require.NotEqual(t, free.BaseUrl, provider.BaseUrl)
	require.NotEqual(t, paid.BaseUrl, provider.BaseUrl)

	resolvedProvider, resolvedModel, err := resolveProvider(ctx, q, db.Agent{ModelGroupID: &group.ID})
	require.NoError(t, err)
	require.Equal(t, provider.BaseUrl, resolvedProvider.BaseUrl)
	require.Equal(t, model, resolvedModel)
}

func TestModelGroupPurposeResolutionRejectsEmptyGroup(t *testing.T) {
	database := modelResolverTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	owner, err := q.CreateUser(ctx, "empty-model-group@test.local")
	require.NoError(t, err)
	group, err := q.CreateModelGroup(ctx, db.ModelGroup{Name: "Empty", Slug: "empty"})
	require.NoError(t, err)
	require.NoError(t, database.Create(&db.DefaultModelSetting{
		Purpose: db.PurposeTaskOrchestrator, UserID: &owner.ID, ModelGroupID: &group.ID,
	}).Error)

	_, _, err = NewNativeEngine(database, nil).resolveRequiredPurposeModel(ctx, owner.ID, db.PurposeTaskOrchestrator)
	require.ErrorContains(t, err, `model group for "task_orchestrator" has no usable members`)
}
