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
