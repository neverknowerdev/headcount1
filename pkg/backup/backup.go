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
)

type BackupManifest struct {
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Items     int       `json:"items"`
}

func CreateBackup(basePath string) (string, error) {
	return CreateBackupWithContext(context.Background(), basePath)
}

func CreateBackupWithContext(ctx context.Context, basePath string) (string, error) {
	log.Println("Starting backup...")

	backupDir := filepath.Join(basePath, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	timestamp := time.Now().Format("2006-01-02_150405")
	archiveName := fmt.Sprintf("backup_%s.tar.gz", timestamp)
	archivePath := filepath.Join(backupDir, archiveName)

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
		Version:   "1.0",
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

	// Backup filesystem data (companies, projects, skills, logs, etc.)
	if err := addDirectoryToTar(tarWriter, filepath.Join(basePath, "data"), "data", &itemCount); err != nil {
		log.Printf("Warning: failed to backup data directory: %v", err)
	}

	// Backup workspace directory (task workspaces, memory, metadata)
	if err := addDirectoryToTar(tarWriter, filepath.Join(basePath, "workspace"), "workspace", &itemCount); err != nil {
		log.Printf("Warning: failed to backup workspace directory: %v", err)
	}

	// Backup companies directory (Manager format: settings, sprints, tasks, comments)
	if err := addDirectoryToTar(tarWriter, filepath.Join(basePath, "companies"), "companies", &itemCount); err != nil {
		log.Printf("Warning: failed to backup companies directory: %v", err)
	}

	// Update manifest with item count
	manifest.Items = itemCount
	manifestBytes, _ = json.MarshalIndent(manifest, "", "  ")

	// Clean up old backups (keep last 7)
	cleanupOldBackups(backupDir, 7)

	log.Printf("Backup completed: %s (%d items)", archivePath, itemCount)
	return archivePath, nil
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
