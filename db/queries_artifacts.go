package db

import "context"

func (q *Queries) CreateArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *Queries) ListArtifactsByTask(ctx context.Context, taskID int32) ([]Artifact, error) {
	var artifacts []Artifact
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&artifacts).Error
	return artifacts, err
}
