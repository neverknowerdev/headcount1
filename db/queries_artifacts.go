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

func (q *Queries) GetArtifact(ctx context.Context, id int32) (Artifact, error) {
	var a Artifact
	err := q.db.WithContext(ctx).First(&a, id).Error
	return a, err
}

// ListArtifactsByTaskTree returns the artifacts of a task and all its
// subtasks, so orchestrator sessions see everything their delegations
// produced.
func (q *Queries) ListArtifactsByTaskTree(ctx context.Context, taskID int32) ([]Artifact, error) {
	taskIDs := []int32{taskID}
	var subtaskIDs []int32
	if err := q.db.WithContext(ctx).Model(&Task{}).Where("parent_id = ?", taskID).Pluck("id", &subtaskIDs).Error; err == nil {
		taskIDs = append(taskIDs, subtaskIDs...)
	}
	var artifacts []Artifact
	err := q.db.WithContext(ctx).Where("task_id IN ?", taskIDs).Order("created_at asc").Find(&artifacts).Error
	return artifacts, err
}

// MarkTaskTreeArtifactsVerified flags every artifact of the task and its
// subtasks as verified. Called when a QA verification session passes all of
// the task's spec items.
func (q *Queries) MarkTaskTreeArtifactsVerified(ctx context.Context, taskID int32) error {
	var subtaskIDs []int32
	taskIDs := []int32{taskID}
	if err := q.db.WithContext(ctx).Model(&Task{}).Where("parent_id = ?", taskID).Pluck("id", &subtaskIDs).Error; err == nil {
		taskIDs = append(taskIDs, subtaskIDs...)
	}
	return q.db.WithContext(ctx).Model(&Artifact{}).Where("task_id IN ?", taskIDs).Update("is_verified", true).Error
}

// GetArtifactByTaskAndFilename returns the newest artifact with the given
// filename on a task, or gorm.ErrRecordNotFound. Used for overwrite detection
// in write_artifact.
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
