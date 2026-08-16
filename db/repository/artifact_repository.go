package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type ArtifactRepository struct{ db *gorm.DB }

func NewArtifactRepository(db *gorm.DB) *ArtifactRepository { return &ArtifactRepository{db: db} }
func (q *ArtifactRepository) CreateArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *ArtifactRepository) ListArtifactsByTask(ctx context.Context, taskID int32) ([]Artifact, error) {
	var artifacts []Artifact
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&artifacts).Error
	return artifacts, err
}

func (q *ArtifactRepository) GetArtifact(ctx context.Context, id int32) (Artifact, error) {
	var a Artifact
	err := q.db.WithContext(ctx).First(&a, id).Error
	return a, err
}

// ListArtifactsByTaskTree returns the artifacts of a task and all its
// subtasks, so orchestrator sessions see everything their delegations
// produced.
func (q *ArtifactRepository) ListArtifactsByTaskTree(ctx context.Context, taskID int32) ([]Artifact, error) {
	// Walk the whole subtree level by level: delegation can nest (e.g.
	// CEO → CTO → Coder), and artifacts are shared across the full tree.
	taskIDs := []int32{taskID}
	frontier := []int32{taskID}
	for len(frontier) > 0 {
		var subtaskIDs []int32
		if err := q.db.WithContext(ctx).Model(&Task{}).Where("parent_id IN ?", frontier).Pluck("id", &subtaskIDs).Error; err != nil {
			break
		}
		taskIDs = append(taskIDs, subtaskIDs...)
		frontier = subtaskIDs
	}
	var artifacts []Artifact
	err := q.db.WithContext(ctx).Where("task_id IN ?", taskIDs).Order("created_at asc").Find(&artifacts).Error
	return artifacts, err
}

// GetArtifactByTaskAndFilename returns the newest artifact with the given
// filename on a task, or gorm.ErrRecordNotFound. Used for overwrite detection
// in write_artifact.
func (q *ArtifactRepository) GetArtifactByTaskAndFilename(ctx context.Context, taskID int32, filename string) (Artifact, error) {
	var a Artifact
	err := q.db.WithContext(ctx).
		Where("task_id = ? AND filename = ?", taskID, filename).
		Order("created_at desc").
		First(&a).Error
	return a, err
}

// UpdateArtifactContent overwrites an existing artifact's content and records
// which run performed the update.
func (q *ArtifactRepository) UpdateArtifactContent(ctx context.Context, id int32, content string, runID int32) error {
	return q.db.WithContext(ctx).Model(&Artifact{}).Where("id = ?", id).
		Updates(map[string]interface{}{"content": content, "run_id": runID}).Error
}
