package backup_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/backup"
	"agent-orchestrator/pkg/mempalace"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func requireMempalaceBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mempalace-mcp"); err != nil {
		t.Skip("mempalace-mcp not in PATH; skipping palace backup e2e")
	}
}

func setupBackupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(
		&db.Company{}, &db.Project{}, &db.Sprint{}, &db.LLMProvider{},
		&db.Agent{}, &db.Skill{}, &db.Task{}, &db.Comment{},
		&db.Attachment{}, &db.Run{}, &db.ActivityLog{}, &db.ProxyRequestLog{},
	))
	return database
}

// TestBackupRestorePalace verifies that agent memories survive a full
// export → wipe → import cycle:
// 1. Seed a sentinel memory into MemPalace.
// 2. Create a backup (palace dir is included).
// 3. Wipe the palace directory entirely.
// 4. Restore the backup.
// 5. Confirm the sentinel is retrievable from the restored palace.
func TestBackupRestorePalace(t *testing.T) {
	requireMempalaceBinary(t)

	ctx := context.Background()
	palace := filepath.Join(t.TempDir(), "palace")
	t.Setenv("MEMPALACE_PALACE", palace)
	t.Setenv("MEMPALACE_BACKEND", "sqlite_exact")

	basePath := t.TempDir()
	sentinel := fmt.Sprintf("PALACE_BACKUP_SENTINEL_%d", time.Now().UnixNano())

	// 1. Seed a memory into the palace.
	client, err := mempalace.NewClientWithPalace(ctx, palace, "sqlite_exact")
	require.NoError(t, err)
	require.NotNil(t, client, "mempalace client should not be nil")
	err = client.AddDrawer(ctx, "backup-test", "test-room", sentinel)
	require.NoError(t, err, "failed to add sentinel drawer")
	client.Close()

	// Verify the sentinel is there before backup.
	client, err = mempalace.NewClientWithPalace(ctx, palace, "sqlite_exact")
	require.NoError(t, err)
	results, err := client.Search(ctx, sentinel, "", 1)
	require.NoError(t, err)
	require.True(t, mempalace.HasResults(results), "sentinel should be present before backup")
	client.Close()

	// 2. Create backup (includes palace).
	archivePath, err := backup.CreateBackup(basePath)
	require.NoError(t, err, "backup creation failed")
	require.FileExists(t, archivePath)

	// 3. Wipe palace completely.
	require.NoError(t, os.RemoveAll(palace), "failed to wipe palace")
	_, statErr := os.Stat(palace)
	require.True(t, os.IsNotExist(statErr), "palace should be gone after wipe")

	// 4. Restore backup.
	database := setupBackupTestDB(t)
	err = backup.RestoreBackup(archivePath, basePath, database)
	require.NoError(t, err, "backup restore failed")

	// 5. Verify sentinel survived.
	require.DirExists(t, palace, "palace directory should be restored")

	client, err = mempalace.NewClientWithPalace(ctx, palace, "sqlite_exact")
	require.NoError(t, err)
	require.NotNil(t, client, "mempalace client should work after restore")
	defer client.Close()

	results, err = client.Search(ctx, sentinel, "", 1)
	require.NoError(t, err)
	require.True(t, mempalace.HasResults(results), "sentinel should be present after restore")
}

// TestBackupPalaceNotIncludedWhenMissing verifies that a backup succeeds even
// when the palace directory doesn't exist (new install, no memories yet).
func TestBackupPalaceNotIncludedWhenMissing(t *testing.T) {
	// Point to a non-existent palace so DefaultPalaceDir returns a missing path.
	t.Setenv("MEMPALACE_PALACE", filepath.Join(t.TempDir(), "nonexistent-palace"))

	basePath := t.TempDir()
	archivePath, err := backup.CreateBackup(basePath)
	require.NoError(t, err, "backup should succeed even with no palace")
	require.FileExists(t, archivePath)
}
