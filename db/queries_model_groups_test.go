package db_test

import (
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupModelGroupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.EnsureSchema(database))
	return database
}

// TestExpandModelGroupMembers verifies AllModels members expand to one
// concrete member per model in the provider's SupportedModels, preserving
// IsFree, while concrete members pass through unchanged.
func TestExpandModelGroupMembers(t *testing.T) {
	members := []db.ModelGroupMember{
		{ProviderID: 1, Model: "fixed-model", IsFree: false},
		{ProviderID: 2, AllModels: true, IsFree: true, Provider: db.LLMProvider{SupportedModels: "m1, m2 ,,m3"}},
	}
	expanded := db.ExpandModelGroupMembers(members)

	require.Len(t, expanded, 4)
	assert.Equal(t, "fixed-model", expanded[0].Model)
	assert.False(t, expanded[0].AllModels)

	var gotModels []string
	for _, m := range expanded[1:] {
		assert.Equal(t, int32(2), m.ProviderID)
		assert.True(t, m.IsFree)
		assert.False(t, m.AllModels)
		gotModels = append(gotModels, m.Model)
	}
	assert.Equal(t, []string{"m1", "m2", "m3"}, gotModels)
}

// TestExpandModelGroupMembers_EmptyCatalog verifies a wildcard member for a
// provider with no supported models yet expands to nothing, rather than a
// single bogus empty-string model.
func TestExpandModelGroupMembers_EmptyCatalog(t *testing.T) {
	members := []db.ModelGroupMember{
		{ProviderID: 1, AllModels: true, Provider: db.LLMProvider{SupportedModels: ""}},
	}
	assert.Empty(t, db.ExpandModelGroupMembers(members))
}
