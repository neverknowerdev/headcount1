package models

import "time"

type Comment struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	TaskID      int32     `json:"task_id" gorm:"not null"`
	Task        Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	AuthorType  string    `json:"author_type" gorm:"not null"`
	AuthorID    *int32    `json:"author_id"`
	Content     string    `json:"content" gorm:"not null"`
	CommentType string    `json:"comment_type" gorm:"default:''"`
	RunID       *int32    `json:"run_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
