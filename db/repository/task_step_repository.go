package repository

import (
	"context"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

// TaskStepRepository reads a task's journal. Steps are written inside
// workflow transitions (see WorkflowTx.AppendStep); AppendTaskStep exists for
// the few entries that record an event without changing the task's state.
type TaskStepRepository struct{ db *gorm.DB }

func NewTaskStepRepository(db *gorm.DB) *TaskStepRepository {
	return &TaskStepRepository{db: db}
}

func (q *TaskStepRepository) AppendTaskStep(ctx context.Context, step TaskStep) (TaskStep, error) {
	err := q.db.WithContext(ctx).Create(&step).Error
	return step, err
}

func (q *TaskStepRepository) GetTaskStep(ctx context.Context, id int64) (TaskStep, error) {
	var step TaskStep
	err := q.db.WithContext(ctx).First(&step, id).Error
	return step, err
}

// ListTaskStepsByKind returns the task's steps of the given kinds, in order.
func (q *TaskStepRepository) ListTaskStepsByKind(ctx context.Context, taskID int32, kinds ...string) ([]TaskStep, error) {
	var steps []TaskStep
	err := q.db.WithContext(ctx).Where("task_id = ? AND kind IN ?", taskID, kinds).Order("id").Find(&steps).Error
	return steps, err
}

// ListTaskStepHeads returns a task's whole journal in order without the bulky
// prompt and response of each smart call. It is what the driver reads to
// decide a task's next step.
func (q *TaskStepRepository) ListTaskStepHeads(ctx context.Context, taskID int32) ([]TaskStep, error) {
	var steps []TaskStep
	err := q.db.WithContext(ctx).Omit("prompt", "response").Where("task_id = ?", taskID).Order("id").Find(&steps).Error
	return steps, err
}

// ListStepsByKindForTasks returns the steps of one kind across several tasks,
// in order: the checkpoints of a set of subtasks, for example.
func (q *TaskStepRepository) ListStepsByKindForTasks(ctx context.Context, taskIDs []int32, kind string) ([]TaskStep, error) {
	var steps []TaskStep
	if len(taskIDs) == 0 {
		return steps, nil
	}
	err := q.db.WithContext(ctx).Omit("prompt", "response").
		Where("task_id IN ? AND kind = ?", taskIDs, kind).Order("id").Find(&steps).Error
	return steps, err
}
