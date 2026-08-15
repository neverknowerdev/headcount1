package repository

import (
	"context"
	"time"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type RunEventRepository struct{ db *gorm.DB }

func NewRunEventRepository(db *gorm.DB) *RunEventRepository {
	return &RunEventRepository{db: db}
}

func (r *RunEventRepository) HasRecentRunEvent(ctx context.Context, runID int32, eventType RunEventType, since time.Time) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&RunEvent{}).
		Where("run_id = ? AND event_type = ? AND created_at >= ?", runID, eventType, since).
		Count(&count).Error
	return count > 0, err
}

func (r *RunEventRepository) EnqueueRunEvent(ctx context.Context, event RunEvent) error {
	if event.DedupeKey != "" {
		var existing RunEvent
		if err := r.db.WithContext(ctx).Where("dedupe_key = ?", event.DedupeKey).First(&existing).Error; err == nil {
			return nil
		}
	}
	return r.db.WithContext(ctx).Create(&event).Error
}

func (r *RunEventRepository) ListPendingRunEvents(ctx context.Context, taskID int32) ([]RunEvent, error) {
	var events []RunEvent
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND consumed_at IS NULL AND event_type IN ?", taskID, []RunEventType{RunEventTypeLifecycleStatus, RunEventTypeStatusReport, RunEventTypeWorkerQuestion}).
		Order("created_at asc").Find(&events).Error
	return events, err
}

func (r *RunEventRepository) ListPendingRunEventsForRun(ctx context.Context, runID int32, eventType RunEventType) ([]RunEvent, error) {
	var events []RunEvent
	err := r.db.WithContext(ctx).
		Where("run_id = ? AND event_type = ? AND consumed_at IS NULL", runID, eventType).
		Order("created_at asc").Find(&events).Error
	return events, err
}

func (r *RunEventRepository) ConsumeRunEvents(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&RunEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).
		Update("consumed_at", time.Now()).Error
}
