package db_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureDefaultModelSettings_SeedsBothPurposesUnconfigured verifies both
// known purposes get an unconfigured row on first call (falling back to the
// calling session's own LLM), and that a second call never overwrites a
// purpose a user has since configured.
func TestEnsureDefaultModelSettings_SeedsBothPurposesUnconfigured(t *testing.T) {
	database := setupModelGroupTestDB(t)
	require.NoError(t, db.EnsureSchema(database))
	q := db.New(database)
	ctx := context.Background()

	user, err := q.CreateUser(ctx, "seed@test.local")
	require.NoError(t, err)
	uid := user.ID

	require.NoError(t, q.EnsureDefaultModelSettingsForUser(ctx, uid))

	for _, purpose := range []string{db.PurposeCommitMessages, db.PurposeAskArtifact} {
		s, err := q.GetDefaultModelSetting(ctx, uid, purpose)
		require.NoError(t, err, "purpose %q should exist", purpose)
		assert.Nil(t, s.ProviderID)
		assert.Nil(t, s.ModelGroupID)
	}

	// User points commit_messages at a fixed provider+model directly.
	customProvider := db.LLMProvider{Name: "Custom", DefaultModel: "x"}
	require.NoError(t, database.Create(&customProvider).Error)
	_, err = q.UpdateDefaultModelSetting(ctx, uid, db.PurposeCommitMessages, &customProvider.ID, "custom-model", nil)
	require.NoError(t, err)

	// Re-seeding must not clobber that override.
	require.NoError(t, q.EnsureDefaultModelSettingsForUser(ctx, uid))
	s, err := q.GetDefaultModelSetting(ctx, uid, db.PurposeCommitMessages)
	require.NoError(t, err)
	require.NotNil(t, s.ProviderID)
	assert.Equal(t, customProvider.ID, *s.ProviderID)
	assert.Equal(t, "custom-model", s.Model)
	assert.Nil(t, s.ModelGroupID)
}
