package repository

import (
	"context"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

// DecisionRepository stores what agents decided, assumed and abandoned.
// Smart-step decisions are written inside the step's workflow transition (see
// WorkflowTx.AddDecision); executor checkpoints record theirs here directly,
// since a decision is not task state.
type DecisionRepository struct{ db *gorm.DB }

func NewDecisionRepository(db *gorm.DB) *DecisionRepository {
	return &DecisionRepository{db: db}
}

func (q *DecisionRepository) CreateDecision(ctx context.Context, d Decision) (Decision, error) {
	err := q.db.WithContext(ctx).Create(&d).Error
	return d, err
}

func (q *DecisionRepository) ListDecisionsByTask(ctx context.Context, taskID int32) ([]Decision, error) {
	var list []Decision
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("id").Find(&list).Error
	return list, err
}

// ListDecisionsByRoot returns every decision recorded anywhere in a task tree,
// in the order they were made.
func (q *DecisionRepository) ListDecisionsByRoot(ctx context.Context, rootTaskID int32) ([]Decision, error) {
	var list []Decision
	err := q.db.WithContext(ctx).Where("root_task_id = ?", rootTaskID).Order("id").Find(&list).Error
	return list, err
}
