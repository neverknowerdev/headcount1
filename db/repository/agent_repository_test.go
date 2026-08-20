package repository_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/pkg/agentdefaults"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnsureBuiltinAgentsForCompany_IsCompleteAndIdempotent(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	company := db.Company{Name: "Acme"}
	require.NoError(t, database.Create(&company).Error)
	provider := db.LLMProvider{Name: "Test", BaseUrl: "https://example.test", DefaultModel: "test-model", ApiKeyEncrypted: ""}
	require.NoError(t, database.Create(&provider).Error)
	q := db.New(database)
	defaults := agentdefaults.Rows(company.ID)
	require.Len(t, defaults, 13)
	require.NoError(t, q.EnsureBuiltinAgentsForCompany(context.Background(), company.ID, defaults, &provider.ID, "test-model"))
	require.NoError(t, q.EnsureBuiltinAgentsForCompany(context.Background(), company.ID, defaults, &provider.ID, "test-model"))

	agents, err := q.ListAgentsByCompany(context.Background(), company.ID)
	require.NoError(t, err)
	require.Len(t, agents, 13)
	for _, agent := range agents {
		assert.True(t, agent.Builtin, agent.Name)
		assert.True(t, agent.Enabled, agent.Name)
		assert.Equal(t, provider.ID, *agent.ProviderID, agent.Name)
		assert.NotEmpty(t, agent.SystemPrompt, agent.Name)
	}

	var coder db.Agent
	require.NoError(t, database.Where("role_key = ?", "Coder").First(&coder).Error)
	assert.Contains(t, coder.Permissions, "browser_use")
	assert.NotContains(t, coder.Permissions, "write\\\":\\\"deny")
}

func TestDeleteAgentRepositoryRemovesOnlyRequestedRow(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "Acme"}
	require.NoError(t, database.Create(&company).Error)
	q := db.New(database)
	custom, err := q.CreateAgent(context.Background(), db.Agent{CompanyID: company.ID, Name: "Custom", SystemPrompt: "test"})
	require.NoError(t, err)
	require.NoError(t, q.DeleteAgent(context.Background(), custom.ID))
	_, err = q.GetAgent(context.Background(), custom.ID)
	assert.Error(t, err)
}
