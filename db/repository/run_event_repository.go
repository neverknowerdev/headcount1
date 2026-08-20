package repository

import (
	"context"
	"errors"
	"fmt"
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
	_, err := r.EnqueueRunEventReturning(ctx, event)
	return err
}

// EnqueueRunEventReturning persists an event and returns the stored row,
// including its database-assigned ID. The ID is the routed message_id for
// routed session messages. Dedupe is safe under concurrent delivery because
// the migration adds a unique partial index for non-empty keys.
func (r *RunEventRepository) EnqueueRunEventReturning(ctx context.Context, event RunEvent) (RunEvent, error) {
	if event.SourceRunID == nil && event.RunID != 0 {
		source := event.RunID
		event.SourceRunID = &source
	}
	if event.DedupeKey != "" {
		var existing RunEvent
		if err := r.db.WithContext(ctx).Where("dedupe_key = ?", event.DedupeKey).First(&existing).Error; err == nil {
			return existing, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return RunEvent{}, err
		}
	}
	if err := r.db.WithContext(ctx).Create(&event).Error; err != nil {
		if event.DedupeKey != "" {
			var existing RunEvent
			if lookupErr := r.db.WithContext(ctx).Where("dedupe_key = ?", event.DedupeKey).First(&existing).Error; lookupErr == nil {
				return existing, nil
			}
		}
		return RunEvent{}, err
	}
	return event, nil
}

// EnqueueRoutedEvent writes a message with an explicit producer and target.
// RunID is retained as the source for compatibility with old event readers.
func (r *RunEventRepository) EnqueueRoutedEvent(ctx context.Context, taskID, sourceRunID, targetRunID int32, eventType RunEventType, payload, dedupeKey string) (RunEvent, error) {
	return r.EnqueueRunEventReturning(ctx, RunEvent{
		TaskID: taskID, RunID: sourceRunID, SourceRunID: &sourceRunID,
		TargetRunID: &targetRunID, EventType: eventType, Payload: payload, DedupeKey: dedupeKey,
	})
}

func (r *RunEventRepository) ListPendingRunEvents(ctx context.Context, taskID int32) ([]RunEvent, error) {
	var events []RunEvent
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND consumed_at IS NULL AND event_type IN ?", taskID, []RunEventType{RunEventTypeLifecycleStatus, RunEventTypeStatusReport, RunEventTypeWorkerQuestion, RunEventTypeHumanInputRequested, RunEventTypeHumanInputAnswered}).
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

// ListUnconsumedEventsForTarget returns unconsumed inbound messages for a run.
// With no event types it returns all targeted, unconsumed events.
func (r *RunEventRepository) ListUnconsumedEventsForTarget(ctx context.Context, targetRunID int32, eventTypes ...RunEventType) ([]RunEvent, error) {
	query := r.db.WithContext(ctx).
		Where("target_run_id = ? AND consumed_at IS NULL", targetRunID).
		Order("created_at asc, id asc")
	if len(eventTypes) > 0 {
		query = query.Where("event_type IN ?", eventTypes)
	}
	var events []RunEvent
	return events, query.Find(&events).Error
}

// AnswerPendingMessage inserts one correlated answer and consumes its request
// in one transaction. Duplicate retries return the existing answer event.
func (r *RunEventRepository) AnswerPendingMessage(ctx context.Context, targetRunID int32, messageID int64, payload, dedupeKey string) (RunEvent, error) {
	if messageID <= 0 {
		return RunEvent{}, fmt.Errorf("message_id must be positive")
	}
	var answer RunEvent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var request RunEvent
		if err := tx.Where("id = ?", messageID).First(&request).Error; err != nil {
			return fmt.Errorf("message %d: %w", messageID, err)
		}
		if request.TargetRunID == nil || *request.TargetRunID != targetRunID {
			return fmt.Errorf("message %d is not addressed to run %d", messageID, targetRunID)
		}
		if request.EventType != RunEventTypeSessionMessage {
			return fmt.Errorf("message %d is not a session message", messageID)
		}
		if request.ConsumedAt != nil {
			if err := tx.Where("reply_to_event_id = ?", messageID).First(&answer).Error; err == nil {
				return nil
			}
			return fmt.Errorf("message %d has already been consumed", messageID)
		}
		now := time.Now()
		source := targetRunID
		answer = RunEvent{
			TaskID: request.TaskID, RunID: targetRunID, SourceRunID: &source,
			TargetRunID: request.SourceRunID, ReplyToEventID: &messageID,
			EventType: RunEventTypeSessionAnswer, Payload: payload, DedupeKey: dedupeKey,
		}
		if err := tx.Create(&answer).Error; err != nil {
			var existing RunEvent
			if lookupErr := tx.Where("reply_to_event_id = ?", messageID).First(&existing).Error; lookupErr == nil {
				answer = existing
				return nil
			}
			return err
		}
		if err := tx.Model(&RunEvent{}).Where("id = ? AND consumed_at IS NULL", messageID).Update("consumed_at", now).Error; err != nil {
			return err
		}
		return nil
	})
	return answer, err
}

func (r *RunEventRepository) FindAnswerForMessage(ctx context.Context, messageID int64) (RunEvent, error) {
	var answer RunEvent
	err := r.db.WithContext(ctx).Where("reply_to_event_id = ? AND event_type = ?", messageID, RunEventTypeSessionAnswer).First(&answer).Error
	return answer, err
}

func (r *RunEventRepository) ConsumeRunEvents(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&RunEvent{}).Where("id IN ? AND consumed_at IS NULL", ids).
		Update("consumed_at", time.Now()).Error
}
