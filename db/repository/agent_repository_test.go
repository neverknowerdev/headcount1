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
	q := db.New(database)
	defaults := agentdefaults.Rows(company.ID)
	require.Len(t, defaults, 13)
	require.NoError(t, q.EnsureBuiltinAgentsForCompany(context.Background(), company.ID, defaults))
	require.NoError(t, q.EnsureBuiltinAgentsForCompany(context.Background(), company.ID, defaults))

	agents, err := q.ListAgentsByCompany(context.Background(), company.ID)
	require.NoError(t, err)
	require.Len(t, agents, 13)
	for _, agent := range agents {
		assert.True(t, agent.Builtin, agent.Name)
		assert.True(t, agent.Enabled, agent.Name)
		assert.Equal(t, "You are the "+agent.Name+" agent.", agent.SystemPrompt)
	}

	// A company's own additions to a built-in role survive later seeding, and a
	// built-in that was switched off is switched back on.
	require.NoError(t, database.Model(&db.Agent{}).Where("company_id = ? AND role_key = ?", company.ID, "CEO").
		Updates(map[string]interface{}{"enabled": false, "system_prompt": "You are the CEO agent. We sell boats."}).Error)
	require.NoError(t, q.EnsureBuiltinAgentsForCompany(context.Background(), company.ID, defaults))
	var ceo db.Agent
	require.NoError(t, database.Where("company_id = ? AND role_key = ?", company.ID, "CEO").First(&ceo).Error)
	assert.True(t, ceo.Enabled, "existing built-ins are re-enabled when catalog seeding runs")
	assert.Equal(t, "You are the CEO agent. We sell boats.", ceo.SystemPrompt)
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
