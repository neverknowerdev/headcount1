package db

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// legacyRunSnapshot is intentionally not a model. It is only the read shape
// used to move the short-lived recovery metadata from the old one-to-one table
// into runs. New installations never create run_snapshots.
type legacyRunSnapshot struct {
	RunID                int32           `gorm:"column:run_id"`
	CheckpointSequence   int64           `gorm:"column:checkpoint_sequence"`
	CheckpointVersion    int             `gorm:"column:checkpoint_version"`
	CheckpointPhase      CheckpointPhase `gorm:"column:checkpoint_phase"`
	RecoveryReason       string          `gorm:"column:recovery_reason"`
	RecoveryInitiator    string          `gorm:"column:recovery_initiator"`
	RecoveryTarget       string          `gorm:"column:recovery_target"`
	ResumeLeaseOwner     string          `gorm:"column:resume_lease_owner"`
	ResumeLeaseUntil     *time.Time      `gorm:"column:resume_lease_until"`
	ResumePreviousStatus string          `gorm:"column:resume_previous_status"`
	ResumeAttempts       int             `gorm:"column:resume_attempts"`
	LastResumeError      string          `gorm:"column:last_resume_error"`
}

func (legacyRunSnapshot) TableName() string { return "run_snapshots" }

// MigrateRunRecoveryToRuns folds the retired RunSnapshot table into Run. It
// is additive and idempotent: existing rows are copied once, then the legacy
// table is removed so it cannot become a second source of truth.
func MigrateRunRecoveryToRuns(database *gorm.DB) error {
	if database == nil || !database.Migrator().HasTable("run_snapshots") {
		return nil
	}
	return database.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		var rows []legacyRunSnapshot
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Model(&Run{}).Where("id = ?", row.RunID).Updates(map[string]interface{}{
				"checkpoint_sequence":    row.CheckpointSequence,
				"checkpoint_version":     row.CheckpointVersion,
				"checkpoint_phase":       row.CheckpointPhase,
				"recovery_reason":        row.RecoveryReason,
				"recovery_initiator":     row.RecoveryInitiator,
				"recovery_target":        row.RecoveryTarget,
				"resume_lease_owner":     row.ResumeLeaseOwner,
				"resume_lease_until":     row.ResumeLeaseUntil,
				"resume_previous_status": row.ResumePreviousStatus,
				"resume_attempts":        row.ResumeAttempts,
				"last_resume_error":      row.LastResumeError,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Migrator().DropTable("run_snapshots")
	})
}
