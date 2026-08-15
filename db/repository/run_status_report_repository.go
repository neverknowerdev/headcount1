package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type RunStatusReportRepository struct{ db *gorm.DB }

func NewRunStatusReportRepository(db *gorm.DB) *RunStatusReportRepository {
	return &RunStatusReportRepository{db: db}
}

func (r *RunStatusReportRepository) RecordRunStatusReport(ctx context.Context, runID int32, status string, messageID int64) error {
	now := time.Now()
	eventTaskID := int32(0)
	var currentRun Run
	if err := r.db.WithContext(ctx).Select("task_id").First(&currentRun, runID).Error; err == nil {
		eventTaskID = NewRunRepository(r.db).rootTaskID(ctx, currentRun.TaskID)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run Run
		if err := tx.First(&run, runID).Error; err != nil {
			return err
		}
		report := RunStatusReport{RunID: runID, Status: status, MessageID: messageID, ReportedAt: now}
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		if run.ParentRunID != nil {
			if eventTaskID == 0 {
				eventTaskID = run.TaskID
			}
			payload, _ := json.Marshal(map[string]interface{}{"status": status, "message_id": messageID, "reported_at": now.Format(time.RFC3339Nano)})
			event := RunEvent{TaskID: eventTaskID, RunID: runID, EventType: RunEventTypeStatusReport, Payload: string(payload), DedupeKey: fmt.Sprintf("run:%d:status-report:%d", runID, report.ID)}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
		}
		return tx.Model(&Run{}).Where("id = ?", runID).Update("current_status", status).Error
	})
}

func (r *RunStatusReportRepository) GetLatestRunStatusReport(ctx context.Context, runID int32) (RunStatusReport, error) {
	var report RunStatusReport
	err := r.db.WithContext(ctx).Where("run_id = ?", runID).Order("reported_at DESC, id DESC").First(&report).Error
	return report, err
}

func (r *RunStatusReportRepository) ListRunStatusReports(ctx context.Context, runID int32) ([]RunStatusReport, error) {
	var reports []RunStatusReport
	err := r.db.WithContext(ctx).Where("run_id = ?", runID).Order("reported_at ASC, id ASC").Find(&reports).Error
	return reports, err
}
