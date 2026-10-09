package db_test

import (
	"agent-orchestrator/db/migrations"
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
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
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
	expanded := db.ExpandModelGroupMembers(db.ModelGroup{Members: members})

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
	assert.Empty(t, db.ExpandModelGroupMembers(db.ModelGroup{Members: members}))
}

// A group routes between models of one kind. A member that stands for all of
// a provider's models stands for those of the group's kind, and a model of
// the other kind that found its way into the rows is never routed to.
func TestExpandModelGroupMembers_KeepsToTheGroupsKind(t *testing.T) {
	zen := db.LLMProvider{SupportedModels: "big-pickle,deepseek-v4-flash", SystemOneModels: "jev-1.13,jev-1.13-free"}
	members := []db.ModelGroupMember{
		{ProviderID: 1, AllModels: true, Provider: zen},
		{ProviderID: 2, Model: "jev-latest"},
		{ProviderID: 3, Model: "gpt-oss-120b"},
	}
	modelsOf := func(group db.ModelGroup) []string {
		var out []string
		for _, member := range db.ExpandModelGroupMembers(group) {
			out = append(out, member.Model)
		}
		return out
	}
	assert.Equal(t, []string{"jev-1.13", "jev-1.13-free", "jev-latest"}, modelsOf(db.ModelGroup{Kind: db.ModelKindSystemOne, Members: members}))
	assert.Equal(t, []string{"big-pickle", "deepseek-v4-flash", "gpt-oss-120b"}, modelsOf(db.ModelGroup{Kind: db.ModelKindLLM, Members: members}))
	assert.Equal(t, []string{"big-pickle", "deepseek-v4-flash", "gpt-oss-120b"}, modelsOf(db.ModelGroup{Members: members}), "a group from before kinds existed holds language models")
}

// Providers list System One models among the others with nothing but the ID
// to tell them apart, so the ID decides, and a catalog is kept in two lists.
func TestSystemOneModelsAreToldApartByTheirID(t *testing.T) {
	for _, id := range []string{"jev-1.13", "jev-1.13-free", "jev-latest", "typesafe/jev-1.13", "JEV-1.13.0", "jev"} {
		assert.True(t, db.IsSystemOneModel(id), id)
	}
	for _, id := range []string{"", "gpt-oss-120b", "deepseek-v4-flash", "jevons-7b", "big-pickle", "claude-fable-5", "openai/gpt-6-sol"} {
		assert.False(t, db.IsSystemOneModel(id), id)
	}

	// However the catalog was written, each model ends up under its kind and
	// the default is a language model when the provider has one.
	zen := db.LLMProvider{SupportedModels: "jev-1.13-free, big-pickle,jev-1.13,deepseek-v4-flash", DefaultModel: "jev-1.13-free"}
	assert.True(t, zen.SortCatalog())
	assert.Equal(t, "big-pickle,deepseek-v4-flash", zen.SupportedModels)
	assert.Equal(t, "jev-1.13-free,jev-1.13", zen.SystemOneModels)
	assert.Equal(t, "big-pickle", zen.DefaultModel)
	assert.False(t, zen.SortCatalog(), "sorting a sorted catalog changes nothing")

	typesafe := db.LLMProvider{SupportedModels: "jev-latest", DefaultModel: "jev-latest"}
	typesafe.SortCatalog()
	assert.Equal(t, "", typesafe.SupportedModels)
	assert.Equal(t, "jev-latest", typesafe.SystemOneModels)
	assert.Equal(t, "jev-latest", typesafe.DefaultModel, "a provider with only System One models defaults to one")
}

// Saving a provider files its models, and a discovered catalog is stored by
// kind; rows from before the kinds were told apart are brought into line.
func TestProviderCatalogsAreStoredByKind(t *testing.T) {
	database := setupModelGroupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	saved, err := q.CreateLLMProvider(ctx, db.LLMProvider{Name: "Zen", BaseUrl: "https://opencode.ai/zen/v1", SupportedModels: "big-pickle,jev-1.13-free"})
	require.NoError(t, err)
	stored, err := q.GetLLMProvider(ctx, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, "big-pickle", stored.SupportedModels)
	assert.Equal(t, "jev-1.13-free", stored.SystemOneModels)
	assert.Equal(t, "big-pickle", stored.DefaultModel)

	require.NoError(t, q.UpdateLLMProviderModelCatalog(ctx, saved.ID, []string{"deepseek-v4-flash", "jev-1.13", "big-pickle", "jev-1.13-free"}))
	stored, err = q.GetLLMProvider(ctx, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, "deepseek-v4-flash,big-pickle", stored.SupportedModels)
	assert.Equal(t, "jev-1.13,jev-1.13-free", stored.SystemOneModels)
	assert.Equal(t, "big-pickle", stored.DefaultModel, "a default that is still offered is kept")

	require.NoError(t, q.ForceUpdateLLMProviderModelCatalog(ctx, saved.ID, []string{"deepseek-v4-flash", "jev-1.13"}))
	stored, err = q.GetLLMProvider(ctx, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, "deepseek-v4-flash", stored.DefaultModel)
	assert.Equal(t, "jev-1.13", stored.SystemOneModels)

	// A row as an earlier version left it: everything in one list.
	require.NoError(t, database.Exec("UPDATE llm_providers SET supported_models = ?, system_one_models = '', default_model = ? WHERE id = ?",
		"jev-1.13-free,big-pickle", "jev-1.13-free", saved.ID).Error)
	require.NoError(t, q.SortLLMProviderCatalogs(ctx))
	stored, err = q.GetLLMProvider(ctx, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, "big-pickle", stored.SupportedModels)
	assert.Equal(t, "jev-1.13-free", stored.SystemOneModels)
	assert.Equal(t, "big-pickle", stored.DefaultModel)
}
