package backup

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

func GetLatestBackup(basePath string) (*time.Time, error) {
	backupDir := filepath.Join(basePath, "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var latestTime *time.Time
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".gz" {
			continue
		}

		// Parse timestamp from filename: backup_2006-01-02_150405.tar.gz,
		// optionally with a collision suffix: backup_2006-01-02_150405-2.tar.gz.
		name := strings.TrimPrefix(entry.Name(), "backup_")
		name = strings.TrimSuffix(name, ".tar.gz")
		if len(name) > len("2006-01-02_150405") {
			name = name[:len("2006-01-02_150405")]
		}

		t, err := time.Parse("2006-01-02_150405", name)
		if err != nil {
			continue
		}

		if latestTime == nil || t.After(*latestTime) {
			latestTime = &t
		}
	}

	return latestTime, nil
}

func ShouldBackupOnStartup(basePath string) bool {
	latest, err := GetLatestBackup(basePath)
	if err != nil {
		log.Printf("Warning: failed to check latest backup: %v", err)
		return true
	}
	if latest == nil {
		return true
	}
	return time.Since(*latest) > 24*time.Hour
}

func StartDailyScheduler(basePath string, database *gorm.DB) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		log.Println("Running scheduled backup...")
		_, err := CreateBackup(basePath, database)
		if err != nil {
			log.Printf("Scheduled backup failed: %v", err)
		}
	}
}
