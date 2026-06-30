package db

import (
	"context"
	"encoding/json"
	"time"
)

func (q *Queries) CreateSession(ctx context.Context, s Session) (Session, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

func (q *Queries) GetSession(ctx context.Context, id int32) (Session, error) {
	var s Session
	err := q.db.WithContext(ctx).First(&s, id).Error
	return s, err
}

func (q *Queries) UpdateSessionStatus(ctx context.Context, id int32, status string) error {
	return q.db.WithContext(ctx).Model(&Session{}).Where("id = ?", id).Update("status", status).Error
}

func (q *Queries) UpdateSessionHistory(ctx context.Context, id int32, history string) error {
	return q.db.WithContext(ctx).Model(&Session{}).Where("id = ?", id).Updates(map[string]interface{}{
		"message_history": history,
		"updated_at":      time.Now(),
	}).Error
}

func (q *Queries) UpdateSessionResult(ctx context.Context, id int32, status, summary string) error {
	return q.db.WithContext(ctx).Model(&Session{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":         status,
		"result_summary": summary,
		"updated_at":     time.Now(),
	}).Error
}

func (q *Queries) GetTaskSessions(ctx context.Context, taskID int32) ([]Session, error) {
	var sessions []Session
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&sessions).Error
	return sessions, err
}

func (q *Queries) GetChildSessions(ctx context.Context, parentSessionID int32) ([]Session, error) {
	var sessions []Session
	err := q.db.WithContext(ctx).Where("parent_session_id = ?", parentSessionID).Order("created_at asc").Find(&sessions).Error
	return sessions, err
}

func (q *Queries) SetTaskMainSession(ctx context.Context, taskID int32, sessionID int32) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", taskID).Update("main_session_id", sessionID).Error
}

// PendingQuestion queries

func (q *Queries) CreatePendingQuestion(ctx context.Context, pq PendingQuestion) (PendingQuestion, error) {
	err := q.db.WithContext(ctx).Create(&pq).Error
	return pq, err
}

func (q *Queries) GetPendingQuestion(ctx context.Context, id int32) (PendingQuestion, error) {
	var pq PendingQuestion
	err := q.db.WithContext(ctx).First(&pq, id).Error
	return pq, err
}

func (q *Queries) AnswerPendingQuestion(ctx context.Context, id int32, answer string) error {
	return q.db.WithContext(ctx).Model(&PendingQuestion{}).Where("id = ?", id).Updates(map[string]interface{}{
		"answer":     answer,
		"status":     "answered",
		"updated_at": time.Now(),
	}).Error
}

func (q *Queries) GetPendingQuestionsForSession(ctx context.Context, toSessionID int32) ([]PendingQuestion, error) {
	var questions []PendingQuestion
	err := q.db.WithContext(ctx).Where("to_session_id = ? AND status = 'pending'", toSessionID).Find(&questions).Error
	return questions, err
}

// SessionMessageHistoryFromJSON parses stored JSON message history.
// Returns nil slice on empty or invalid JSON (caller should start fresh).
func SessionMessageHistoryFromJSON(raw string) []map[string]interface{} {
	if raw == "" {
		return nil
	}
	var msgs []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		return nil
	}
	return msgs
}
