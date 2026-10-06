package updater

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SQLiteSnapshot is the durable database checkpoint associated with one
// deployment attempt.
type SQLiteSnapshot struct {
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}

// CreateSQLiteSnapshot creates a consistent SQLite backup using SQLite's own
// VACUUM INTO implementation. The destination is required to be outside the
// live database path and is atomically replaced only after VACUUM completes.
func CreateSQLiteSnapshot(ctx context.Context, databasePath, destination string) (SQLiteSnapshot, error) {
	if databasePath == "" || destination == "" {
		return SQLiteSnapshot{}, errors.New("database and snapshot paths are required")
	}
	if filepath.Clean(databasePath) == filepath.Clean(destination) {
		return SQLiteSnapshot{}, errors.New("snapshot destination must differ from database")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return SQLiteSnapshot{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".sqlite-snapshot-*")
	if err != nil {
		return SQLiteSnapshot{}, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return SQLiteSnapshot{}, err
	}
	_ = os.Remove(tmpPath)

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return SQLiteSnapshot{}, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return SQLiteSnapshot{}, fmt.Errorf("vacuum into snapshot: %w", err)
	}
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		_ = os.Remove(tmpPath)
		if err == nil {
			err = errors.New("snapshot is empty")
		}
		return SQLiteSnapshot{}, err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = os.Remove(tmpPath)
		return SQLiteSnapshot{}, err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		_ = os.Remove(tmpPath)
		return SQLiteSnapshot{}, err
	}
	return SQLiteSnapshot{Path: destination, CreatedAt: time.Now().UTC(), Size: info.Size()}, nil
}

// RestoreSQLiteSnapshot replaces the live database atomically. The old file is
// retained at databasePath+".failed" until the caller removes it.
func RestoreSQLiteSnapshot(snapshotPath, databasePath string) error {
	if snapshotPath == "" || databasePath == "" {
		return errors.New("snapshot and database paths are required")
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		return fmt.Errorf("snapshot unavailable: %w", err)
	}
	failedPath := databasePath + ".failed"
	_ = os.Remove(failedPath)
	if _, err := os.Stat(databasePath); err == nil {
		if err := os.Rename(databasePath, failedPath); err != nil {
			return fmt.Errorf("preserve failed database: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(snapshotPath, databasePath); err != nil {
		_ = os.Rename(failedPath, databasePath)
		return fmt.Errorf("promote snapshot: %w", err)
	}
	return nil
}
