package models

import "time"

type RunEvent struct {
	ID         int64        `json:"id" gorm:"primaryKey"`
	TaskID     int32        `json:"task_id" gorm:"not null;index"`
	RunID      int32        `json:"run_id" gorm:"not null;index"`
	EventType  RunEventType `json:"event_type" gorm:"not null"`
	Payload    string       `json:"payload" gorm:"type:text"`
	DedupeKey  string       `json:"dedupe_key" gorm:"index"`
	CreatedAt  time.Time    `json:"created_at" gorm:"index"`
	ConsumedAt *time.Time   `json:"consumed_at,omitempty" gorm:"index"`
}
