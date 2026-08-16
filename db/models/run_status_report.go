package models

import "time"

type RunStatusReport struct {
	ID         int64     `json:"id" gorm:"primaryKey"`
	RunID      int32     `json:"run_id" gorm:"not null;index"`
	Run        Run       `json:"-" gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE;"`
	Status     string    `json:"status" gorm:"not null"`
	MessageID  int64     `json:"message_id" gorm:"index"`
	ReportedAt time.Time `json:"reported_at" gorm:"index"`
}
