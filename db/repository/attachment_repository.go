package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type AttachmentRepository struct{ db *gorm.DB }

func NewAttachmentRepository(db *gorm.DB) *AttachmentRepository { return &AttachmentRepository{db: db} }
func (q *AttachmentRepository) CreateAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *AttachmentRepository) ListAttachmentsByTask(ctx context.Context, taskID int32) ([]Attachment, error) {
	var a []Attachment
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&a).Error
	return a, err
}

func (q *AttachmentRepository) ListAllAttachments(ctx context.Context) ([]Attachment, error) {
	var attachments []Attachment
	err := q.db.WithContext(ctx).Order("id").Find(&attachments).Error
	return attachments, err
}
