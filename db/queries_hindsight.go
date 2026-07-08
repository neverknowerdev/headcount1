package db

import (
	"context"

	"gorm.io/gorm/clause"
)

func (q *Queries) ListHindsightDocsByProject(ctx context.Context, projectID int32) ([]HindsightDocument, error) {
	var docs []HindsightDocument
	err := q.db.WithContext(ctx).Where("project_id = ?", projectID).Find(&docs).Error
	return docs, err
}

func (q *Queries) UpsertHindsightDoc(ctx context.Context, doc HindsightDocument) error {
	return q.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"document_id", "sha256", "updated_at"}),
	}).Create(&doc).Error
}

func (q *Queries) DeleteHindsightDoc(ctx context.Context, projectID int32, path string) error {
	return q.db.WithContext(ctx).Where("project_id = ? AND path = ?", projectID, path).Delete(&HindsightDocument{}).Error
}
