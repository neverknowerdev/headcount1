package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
)

type TaskRepository struct{ db *gorm.DB }

func NewTaskRepository(db *gorm.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

const taskGitBranchPrefix = "headcount1/"

func TaskGitBranch(refKey string, taskID int32) string {
	refKey = strings.TrimSpace(refKey)
	if refKey == "" {
		refKey = fmt.Sprintf("TASK-%d", taskID)
	}
	return taskGitBranchPrefix + refKey
}

func (q *TaskRepository) CreateTask(ctx context.Context, t Task) (Task, error) {
	err := q.db.WithContext(ctx).Create(&t).Error
	if err != nil {
		return t, err
	}
	// Assign the human-readable ref key ("DEC-50", subtasks "DEC-50-1", …).
	if t.RefKey == "" {
		if key, kErr := q.computeTaskRefKey(ctx, t); kErr == nil && key != "" {
			t.RefKey = key
			if uErr := q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", t.ID).Update("ref_key", key).Error; uErr != nil {
				fmt.Printf("Warning: failed to store ref_key for task %d: %v\n", t.ID, uErr)
			}
		}
	}
	if err := q.EnsureTaskGitBranch(ctx, &t); err != nil {
		return t, fmt.Errorf("assign task git branch: %w", err)
	}
	return t, err
}

// ensureTaskGitBranch assigns one canonical branch to a task tree. New root
// tasks get a branch from their RefKey; subtasks inherit the root branch.
// Existing non-empty branches are preserved for backwards compatibility.
func (q *TaskRepository) EnsureTaskGitBranch(ctx context.Context, t *Task) error {
	root := *t
	if t.ParentID != nil {
		resolved, err := q.GetRootTask(ctx, t.ID)
		if err != nil {
			return err
		}
		root = resolved
	}

	branch := strings.TrimSpace(root.GitHubBranch)
	if branch == "" {
		branch = TaskGitBranch(root.RefKey, root.ID)
		if err := q.db.WithContext(ctx).Model(&Task{}).
			Where("id = ?", root.ID).Update("git_hub_branch", branch).Error; err != nil {
			return err
		}
	}
	if strings.TrimSpace(t.GitHubBranch) == "" {
		t.GitHubBranch = branch
		if err := q.db.WithContext(ctx).Model(&Task{}).
			Where("id = ?", t.ID).Update("git_hub_branch", branch).Error; err != nil {
			return err
		}
	}
	return nil
}

// computeTaskRefKey builds the task key: main tasks get
// "<COMPANY_SHORT>-<id>"; subtasks get "<parent ref>-<sibling index>" where
// the index is the task's 1-based position among its parent's subtasks.
func (q *TaskRepository) computeTaskRefKey(ctx context.Context, t Task) (string, error) {
	if t.ParentID == nil {
		var company Company
		if err := q.db.WithContext(ctx).First(&company, t.CompanyID).Error; err != nil {
			return "", err
		}
		short := strings.ToUpper(company.ShortName)
		if short == "" {
			short = "TASK"
		}
		return fmt.Sprintf("%s-%d", short, t.ID), nil
	}
	var parent Task
	if err := q.db.WithContext(ctx).First(&parent, *t.ParentID).Error; err != nil {
		return "", err
	}
	parentRef := parent.RefKey
	if parentRef == "" {
		var pErr error
		if parentRef, pErr = q.computeTaskRefKey(ctx, parent); pErr != nil {
			return "", pErr
		}
	}
	// Next sibling index = max index used by existing siblings + 1, so keys
	// never collide even after siblings are deleted.
	var siblings []Task
	if err := q.db.WithContext(ctx).
		Select("id", "ref_key").
		Where("parent_id = ? AND id != ?", *t.ParentID, t.ID).
		Find(&siblings).Error; err != nil {
		return "", err
	}
	maxIdx := 0
	prefix := parentRef + "-"
	for _, s := range siblings {
		if !strings.HasPrefix(s.RefKey, prefix) {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(strings.TrimPrefix(s.RefKey, prefix), "%d", &idx); err == nil && idx > maxIdx {
			maxIdx = idx
		}
	}
	return fmt.Sprintf("%s-%d", parentRef, maxIdx+1), nil
}

func (q *TaskRepository) UpdateTask(ctx context.Context, t Task) (Task, error) {
	var previous Task
	if err := q.db.WithContext(ctx).Select("id", "status", "done_at").First(&previous, t.ID).Error; err == nil {
		if t.Status == TaskStatusDone {
			if t.DoneAt == nil {
				if previous.Status != TaskStatusDone || previous.DoneAt == nil {
					now := time.Now()
					t.DoneAt = &now
				} else {
					t.DoneAt = previous.DoneAt
				}
			}
		} else {
			t.DoneAt = nil
		}
	}
	err := q.db.WithContext(ctx).Save(&t).Error
	return t, err
}

// SetTaskStatusIf changes only the task status when it still has the expected
// value. Lifecycle signals such as a human reply must not Save a stale full
// task row: that could overwrite an archive flag or another concurrent edit.
func (q *TaskRepository) SetTaskStatusIf(ctx context.Context, taskID int32, from, to string) (bool, error) {
	updates := map[string]interface{}{"status": to}
	if to == TaskStatusDone && from != TaskStatusDone {
		updates["done_at"] = time.Now()
	} else if to != TaskStatusDone {
		updates["done_at"] = nil
	}
	result := q.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", taskID, from).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

func (q *TaskRepository) GetTask(ctx context.Context, id int32) (Task, error) {
	var t Task
	err := q.db.WithContext(ctx).Preload("Company").Preload("Project").Preload("Sprint").First(&t, id).Error
	return t, err
}

func (q *TaskRepository) GetTaskByRefKey(ctx context.Context, refKey string) (Task, error) {
	var t Task
	err := q.db.WithContext(ctx).Preload("Company").Preload("Project").Preload("Sprint").Where("ref_key = ?", refKey).First(&t).Error
	return t, err
}

func (q *TaskRepository) DeleteTask(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&Task{}, id).Error
}

func (q *TaskRepository) LockTaskRun(ctx context.Context, taskID int32, runID int32) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND run_id IS NULL", taskID).Update("run_id", runID).Error
}

// ClaimTaskRun atomically claims an unowned task for a newly-created run.
// The boolean is false when another caller won the race.
func (q *TaskRepository) ClaimTaskRun(ctx context.Context, taskID int32, runID int32) (bool, error) {
	result := q.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND run_id IS NULL", taskID).Update("run_id", runID)
	return result.RowsAffected == 1, result.Error
}

func (q *TaskRepository) UnlockTaskRun(ctx context.Context, taskID int32) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", taskID).Update("run_id", nil).Error
}

// SetTaskOrchestratorRun records the current task-level orchestrator root.
// Historical orchestrator runs remain in the runs table; this pointer is the
// durable owner of the task's active monitoring session.
func (q *TaskRepository) SetTaskOrchestratorRun(ctx context.Context, taskID, runID int32) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", taskID).Update("orchestrator_run_id", runID).Error
}

// GetRootTask walks the parent chain from taskID and returns the top-most
// ancestor (the task itself when it has no parent). Bounded to 20 hops to
// guard against cycles.
func (q *TaskRepository) GetRootTask(ctx context.Context, taskID int32) (Task, error) {
	task, err := q.GetTask(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	for hops := 0; task.ParentID != nil && hops < 20; hops++ {
		parent, err := q.GetTask(ctx, *task.ParentID)
		if err != nil {
			return task, nil // parent missing — treat current as root
		}
		task = parent
	}
	return task, nil
}

// UpdateTaskRefinedDescription stores the agent-produced refinement without
// touching the user's original description.
func (q *TaskRepository) UpdateTaskRefinedDescription(ctx context.Context, taskID int32, refined string) error {
	return q.db.WithContext(ctx).Model(&Task{}).Where("id = ?", taskID).
		Update("refined_description", refined).Error
}

func (q *TaskRepository) ListSubtasksByParent(ctx context.Context, parentID int32) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).Preload("Agent").Where("parent_id = ?", parentID).
		Order("created_at ASC").Find(&tasks).Error
	return tasks, err
}

func (q *TaskRepository) CountRunningSubtasks(ctx context.Context, parentID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&Task{}).
		Joins("JOIN runs ON runs.id = tasks.run_id").
		Where("tasks.parent_id = ? AND runs.status = ?", parentID, "running").
		Count(&count).Error
	return count, err
}

func (q *TaskRepository) ListAllTasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).Order("id").Find(&tasks).Error
	return tasks, err
}
