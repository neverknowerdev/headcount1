package migrations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
	require.Equal(t, 44, tables)

	var revisions int
	require.NoError(t, database.QueryRow(`SELECT count(*) FROM atlas_schema_revisions`).Scan(&revisions))
	require.Equal(t, 55, revisions)

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
	require.Equal(t, 55, revisions)

	_ = database.Close()
}
