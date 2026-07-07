package db_test

import (
	"context"
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
	require.NoError(t, database.AutoMigrate(&db.LLMProvider{}, &db.ModelGroup{}, &db.ModelGroupMember{}))
	return database
}

// TestEnsureDefaultModelGroups_SeedsBothGroupsWithOpenRouterAny verifies that
// both built-in groups (Memory Management, Utility) are created and each
// gets a single "any model from OpenRouter" member once the OpenRouter
// provider row exists — and that a second call is a no-op once members
// exist, so user edits are never clobbered.
func TestEnsureDefaultModelGroups_SeedsBothGroupsWithOpenRouterAny(t *testing.T) {
	database := setupModelGroupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	openRouter := db.LLMProvider{Name: "OpenRouter Free Models", BaseUrl: "https://openrouter.ai/api/v1", Builtin: true, ProviderName: db.ProviderVendorOpenRouter}
	require.NoError(t, database.Create(&openRouter).Error)

	require.NoError(t, q.EnsureDefaultModelGroups(ctx))

	for _, slug := range []string{db.DefaultMemoryGroupSlug, db.DefaultUtilityGroupSlug} {
		group, err := q.GetModelGroupByKey(ctx, slug)
		require.NoError(t, err, "group %q should exist", slug)
		assert.True(t, group.Builtin)
		require.Len(t, group.Members, 1, "group %q should have exactly one default member", slug)
		m := group.Members[0]
		assert.Equal(t, openRouter.ID, m.ProviderID)
		assert.True(t, m.AllModels)
		assert.True(t, m.IsFree)
	}

	// User edits the Memory Management group's members — a second call must
	// not overwrite them.
	memGroup, err := q.GetModelGroupByKey(ctx, db.DefaultMemoryGroupSlug)
	require.NoError(t, err)
	custom := db.LLMProvider{Name: "Custom", BaseUrl: "https://example.com/v1"}
	require.NoError(t, database.Create(&custom).Error)
	require.NoError(t, q.ReplaceModelGroupMembers(ctx, memGroup.ID, []db.ModelGroupMember{
		{ProviderID: custom.ID, Model: "custom-model"},
	}))

	require.NoError(t, q.EnsureDefaultModelGroups(ctx))

	memGroup, err = q.GetModelGroupByKey(ctx, db.DefaultMemoryGroupSlug)
	require.NoError(t, err)
	require.Len(t, memGroup.Members, 1)
	assert.Equal(t, "custom-model", memGroup.Members[0].Model, "user-edited members must survive a re-seed call")
}

// TestEnsureDefaultModelGroups_NoOpenRouterProvider verifies groups are still
// created (so they exist for the user to configure) even when the
// OpenRouter provider row hasn't been seeded yet — they simply start empty.
func TestEnsureDefaultModelGroups_NoOpenRouterProvider(t *testing.T) {
	database := setupModelGroupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	require.NoError(t, q.EnsureDefaultModelGroups(ctx))

	group, err := q.GetModelGroupByKey(ctx, db.DefaultUtilityGroupSlug)
	require.NoError(t, err)
	assert.Empty(t, group.Members)
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
