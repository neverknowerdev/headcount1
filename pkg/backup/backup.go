package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"
)

type BackupManifest struct {
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Items     int       `json:"items"`
}

// fileDirs are the real-file directories included in a backup (under the
// files/ prefix), mirroring the runtime layout so a restore is
// ready-to-work. credentials/ is deliberately excluded (raw secrets),
// venv/ is machine-specific and rebuilt by setup, backups/ would recurse,
// and the live db/ ships as db-snapshot/ + the entities/ tree instead.
// Note: "ssh" and "credentials" are deliberately EXCLUDED. They hold cleartext
// private keys (the operator-provisioned shared key and any transiently
// materialized per-user key) — including them would ship usable secrets in an
// unencrypted archive. Per-user SSH keys survive a backup as ciphertext in the
// database (UserGitCredential.SSHPrivateKeyEncrypted); the shared key is
// re-provisioned by the operator on restore.
var fileDirs = []string{"repos", "workspace", "uploads", "artifacts", "logs", "skills"}

func CreateBackup(basePath string, database *gorm.DB) (string, error) {
	return CreateBackupWithContext(context.Background(), basePath, database)
}

func CreateBackupWithContext(ctx context.Context, basePath string, database *gorm.DB) (string, error) {
	log.Println("Starting backup...")

	backupDir := filepath.Join(basePath, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Timestamps are second-granular; if a backup with the same name already
	// exists (two backups within one second), add a numeric suffix instead of
	// silently overwriting the earlier archive. Bounded so an unexpected
	// filesystem condition surfaces as an error, never an endless loop.
	const maxNameAttempts = 100
	timestamp := time.Now().Format("2006-01-02_150405")
	archivePath := ""
	for n := 1; n <= maxNameAttempts; n++ {
		name := fmt.Sprintf("backup_%s.tar.gz", timestamp)
		if n > 1 {
			name = fmt.Sprintf("backup_%s-%d.tar.gz", timestamp, n)
		}
		candidate := filepath.Join(backupDir, name)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			archivePath = candidate
			break
		} else if err != nil {
			return "", fmt.Errorf("failed to check backup name %s: %w", name, err)
		}
	}
	if archivePath == "" {
		return "", fmt.Errorf("could not find a free backup filename for %s after %d attempts", timestamp, maxNameAttempts)
	}

	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to create archive file: %w", err)
	}
	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	itemCount := 0

	// Write manifest
	manifest := BackupManifest{
		Version:   "2",
		Timestamp: time.Now(),
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	writeTarEntry(tarWriter, "backup_manifest.json", manifestBytes)
	itemCount++

	// Backup settings.yaml
	if data, err := os.ReadFile(filepath.Join(basePath, "settings.yaml")); err == nil {
		writeTarEntry(tarWriter, "settings.yaml", data)
		itemCount++
	}

	// Entity JSON tree, generated from the database — the restore source.
	if database != nil {
		n, err := exportEntities(tarWriter, database)
		itemCount += n
		if err != nil {
			return "", fmt.Errorf("failed to export entities: %w", err)
		}
	}

	// SQLite snapshot (VACUUM INTO) — a disaster-recovery artifact only;
	// restore reads the entities/ tree, never this file.
	if database != nil && database.Dialector.Name() == "sqlite" {
		if err := addDBSnapshot(ctx, tarWriter, database); err != nil {
			log.Printf("Warning: failed to snapshot database: %v", err)
		} else {
			itemCount++
		}
	}

	// Real-file directories, under the files/ prefix.
	for _, dir := range fileDirs {
		if err := addDirectoryToTar(tarWriter, filepath.Join(basePath, dir), filepath.Join("files", dir), &itemCount); err != nil {
			log.Printf("Warning: failed to backup %s directory: %v", dir, err)
		}
	}

	// Clean up old backups (keep last 7)
	cleanupOldBackups(backupDir, 7)

	log.Printf("Backup completed: %s (%d items)", archivePath, itemCount)
	return archivePath, nil
}

// addDBSnapshot writes a consistent copy of the live SQLite database into
// the archive as db-snapshot/headcount1.db using VACUUM INTO.
func addDBSnapshot(ctx context.Context, tw *tar.Writer, database *gorm.DB) error {
	tmpDir, err := os.MkdirTemp("", "headcount1-db-snapshot-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	snapshotPath := filepath.Join(tmpDir, "headcount1.db")
	if err := database.WithContext(ctx).Exec("VACUUM INTO ?", snapshotPath).Error; err != nil {
		return err
	}

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return err
	}
	writeTarEntry(tw, "db-snapshot/headcount1.db", data)
	return nil
}

func writeTarEntry(tw *tar.Writer, name string, data []byte) {
	header := &tar.Header{
		Name: name,
		Mode: 0644,
		Size: int64(len(data)),
	}
	tw.WriteHeader(header)
	tw.Write(data)
}

func addDirectoryToTar(tw *tar.Writer, dirPath, tarPrefix string, itemCount *int) error {
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return nil
	}

	return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}
		tarPath := filepath.Join(tarPrefix, relPath)

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = tarPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			io.Copy(tw, file)
			*itemCount++
		}

		return nil
	})
}

func cleanupOldBackups(backupDir string, keepCount int) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return
	}

	var backups []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".gz" {
			backups = append(backups, entry)
		}
	}

	if len(backups) <= keepCount {
		return
	}

	// Sort by name (which includes timestamp)
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].Name() > backups[j].Name() {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	// Delete oldest
	for i := 0; i < len(backups)-keepCount; i++ {
		os.Remove(filepath.Join(backupDir, backups[i].Name()))
	}
}
