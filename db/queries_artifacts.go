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

// GetArtifactByID loads a single artifact.
func (q *Queries) GetArtifactByID(ctx context.Context, id int32) (Artifact, error) {
	var a Artifact
	err := q.db.WithContext(ctx).First(&a, id).Error
	return a, err
}

// GetArtifactByTaskAndFilename returns the newest artifact with the given
// filename on a task, or gorm.ErrRecordNotFound.
func (q *Queries) GetArtifactByTaskAndFilename(ctx context.Context, taskID int32, filename string) (Artifact, error) {
	var a Artifact
	err := q.db.WithContext(ctx).
		Where("task_id = ? AND filename = ?", taskID, filename).
		Order("created_at desc").
		First(&a).Error
	return a, err
}

// UpdateArtifactContent overwrites an existing artifact's content and records
// which run performed the update.
func (q *Queries) UpdateArtifactContent(ctx context.Context, id int32, content string, runID int32) error {
	return q.db.WithContext(ctx).Model(&Artifact{}).Where("id = ?", id).
		Updates(map[string]interface{}{"content": content, "run_id": runID}).Error
}
