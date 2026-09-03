package updater

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
)

func TestSQLiteSnapshotRoundTripPreservesDataAndKeepsFailureEvidence(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "headcount1.db")
	snapshotPath := filepath.Join(root, "deploy", "attempt.db")
	db, err := sql.Open("sqlite", live)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE values_table (value TEXT); INSERT INTO values_table(value) VALUES ('before');")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	snapshot, err := CreateSQLiteSnapshot(context.Background(), live, snapshotPath)
	require.NoError(t, err)
	require.Positive(t, snapshot.Size)

	db, err = sql.Open("sqlite", live)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE values_table SET value = 'candidate';")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	require.NoError(t, RestoreSQLiteSnapshot(snapshot.Path, live))
	db, err = sql.Open("sqlite", live)
	require.NoError(t, err)
	var value string
	require.NoError(t, db.QueryRow("SELECT value FROM values_table").Scan(&value))
	require.Equal(t, "before", value)
	require.NoError(t, db.Close())
}

func TestSQLiteSnapshotRejectsSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	_, err := CreateSQLiteSnapshot(context.Background(), path, path)
	require.Error(t, err)
}
