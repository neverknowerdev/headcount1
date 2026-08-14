package db

import (
	"context"
	"reflect"
	"time"

	"gorm.io/gorm"
)

// legacyRunRecovery is the old column layout used by the first resumable-run
// implementation. It is deliberately not a model: new code has one JSON
// document on runs, while this shape exists only while upgrading an old DB.
type legacyRunRecovery struct {
	ID                   int32           `gorm:"column:id"`
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

func (legacyRunRecovery) TableName() string { return "runs" }

// legacyRunSnapshot is the retired one-to-one table layout. It is read only
// for upgrading databases that briefly used that design; it is never created
// by current code and is removed after its data is folded into runs.recovery.
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

var legacyRunRecoveryColumns = []string{
	"checkpoint_sequence", "checkpoint_version", "checkpoint_phase",
	"recovery_reason", "recovery_initiator", "recovery_target",
	"resume_lease_owner", "resume_lease_until", "resume_previous_status",
	"resume_attempts", "last_resume_error",
}

func tableHasColumn(database *gorm.DB, table, wanted string) bool {
	columns, err := database.Migrator().ColumnTypes(table)
	if err != nil {
		return false
	}
	for _, column := range columns {
		if column.Name() == wanted {
			return true
		}
	}
	return false
}

func recoveryFromLegacy(row legacyRunRecovery) RunRecovery {
	return RunRecovery{
		CheckpointSequence:   row.CheckpointSequence,
		CheckpointVersion:    row.CheckpointVersion,
		CheckpointPhase:      row.CheckpointPhase,
		RecoveryReason:       row.RecoveryReason,
		RecoveryInitiator:    row.RecoveryInitiator,
		RecoveryTarget:       row.RecoveryTarget,
		ResumeLeaseOwner:     row.ResumeLeaseOwner,
		ResumeLeaseUntil:     row.ResumeLeaseUntil,
		ResumePreviousStatus: row.ResumePreviousStatus,
		ResumeAttempts:       row.ResumeAttempts,
		LastResumeError:      row.LastResumeError,
	}
}

func recoveryFromSnapshot(row legacyRunSnapshot) RunRecovery {
	return RunRecovery{
		CheckpointSequence:   row.CheckpointSequence,
		CheckpointVersion:    row.CheckpointVersion,
		CheckpointPhase:      row.CheckpointPhase,
		RecoveryReason:       row.RecoveryReason,
		RecoveryInitiator:    row.RecoveryInitiator,
		RecoveryTarget:       row.RecoveryTarget,
		ResumeLeaseOwner:     row.ResumeLeaseOwner,
		ResumeLeaseUntil:     row.ResumeLeaseUntil,
		ResumePreviousStatus: row.ResumePreviousStatus,
		ResumeAttempts:       row.ResumeAttempts,
		LastResumeError:      row.LastResumeError,
	}
}

// MigrateRunRecoveryToRuns folds all retired recovery layouts into the single
// runs.recovery JSON document. It is additive and idempotent. Conversation
// history is not touched: JSONL logs remain the sole source of message data.
func MigrateRunRecoveryToRuns(database *gorm.DB) error {
	if database == nil || !database.Migrator().HasTable("runs") {
		return nil
	}
	return database.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		// A deployed pre-JSONB database may still have the original columns.
		// Copy them before dropping the columns so upgrades do not lose recovery
		// cursors or an in-flight lease.
		if tableHasColumn(tx, "runs", legacyRunRecoveryColumns[0]) {
			var rows []legacyRunRecovery
			if err := tx.Table("runs").Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				var run Run
				if err := tx.First(&run, row.ID).Error; err != nil {
					return err
				}
				if reflect.DeepEqual(run.Recovery, RunRecovery{}) {
					run.Recovery = recoveryFromLegacy(row)
					if err := tx.Save(&run).Error; err != nil {
						return err
					}
				}
			}
			for _, column := range legacyRunRecoveryColumns {
				if tableHasColumn(tx, "runs", column) {
					// Use the dialect's native ALTER TABLE form rather than
					// GORM's SQLite table-recreation path, which cannot infer a
					// schema for a column no longer represented on Run.
					if err := tx.Exec("ALTER TABLE runs DROP COLUMN " + column).Error; err != nil {
						return err
					}
				}
			}
		}

		if !tx.Migrator().HasTable("run_snapshots") {
			return nil
		}
		var rows []legacyRunSnapshot
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var run Run
			if err := tx.First(&run, row.RunID).Error; err != nil {
				return err
			}
			run.Recovery = recoveryFromSnapshot(row)
			if err := tx.Save(&run).Error; err != nil {
				return err
			}
		}
		return tx.Migrator().DropTable("run_snapshots")
	})
}
