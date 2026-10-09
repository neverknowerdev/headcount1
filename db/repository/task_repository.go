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
	return createTask(q.db.WithContext(ctx), t)
}

// createTask inserts a task and completes the fields derived from its place in
// the tree: its root and depth, its human-readable ref key, and the branch the
// whole tree shares. It runs on whatever handle it is given, so a workflow
// transition can create tasks inside its own transaction.
func createTask(db *gorm.DB, t Task) (Task, error) {
	if t.ParentID != nil {
		var parent Task
		if err := db.Select("id", "root_task_id", "depth").First(&parent, *t.ParentID).Error; err == nil {
			t.RootTaskID = parent.RootTaskID
			if t.RootTaskID == 0 {
				t.RootTaskID = parent.ID
			}
			t.Depth = parent.Depth + 1
		}
	}
	if err := db.Create(&t).Error; err != nil {
		return t, err
	}
	if t.RootTaskID == 0 {
		// A root is its own root; so is a task whose parent no longer exists.
		t.RootTaskID = t.ID
		if err := db.Model(&Task{}).Where("id = ?", t.ID).Update("root_task_id", t.ID).Error; err != nil {
			return t, err
		}
	}
	// Assign the human-readable ref key ("DEC-50", subtasks "DEC-50-1", …).
	if t.RefKey == "" {
		if key, kErr := computeTaskRefKey(db, t); kErr == nil && key != "" {
			t.RefKey = key
			if uErr := db.Model(&Task{}).Where("id = ?", t.ID).Update("ref_key", key).Error; uErr != nil {
				fmt.Printf("Warning: failed to store ref_key for task %d: %v\n", t.ID, uErr)
			}
		}
	}
	if err := ensureTaskGitBranch(db, &t); err != nil {
		return t, fmt.Errorf("assign task git branch: %w", err)
	}
	return t, nil
}

func ensureTaskGitBranch(db *gorm.DB, t *Task) error {
	root := *t
	if t.ParentID != nil {
		resolved, err := getRootTask(db, t.ID)
		if err != nil {
			return err
		}
		root = resolved
	}

	branch := strings.TrimSpace(root.GitHubBranch)
	if branch == "" {
		branch = TaskGitBranch(root.RefKey, root.ID)
		if err := db.Model(&Task{}).
			Where("id = ?", root.ID).Update("git_hub_branch", branch).Error; err != nil {
			return err
		}
	}
	if strings.TrimSpace(t.GitHubBranch) == "" {
		t.GitHubBranch = branch
		if err := db.Model(&Task{}).
			Where("id = ?", t.ID).Update("git_hub_branch", branch).Error; err != nil {
			return err
		}
	}
	return nil
}

// computeTaskRefKey builds the task key: main tasks get
// "<COMPANY_SHORT>-<id>"; subtasks get "<parent ref>-<sibling index>" where
// the index is the task's 1-based position among its parent's subtasks.
func computeTaskRefKey(db *gorm.DB, t Task) (string, error) {
	if t.ParentID == nil {
		var company Company
		if err := db.First(&company, t.CompanyID).Error; err != nil {
			return "", err
		}
		short := strings.ToUpper(company.ShortName)
		if short == "" {
			short = "TASK"
		}
		return fmt.Sprintf("%s-%d", short, t.ID), nil
	}
	var parent Task
	if err := db.First(&parent, *t.ParentID).Error; err != nil {
		return "", err
	}
	parentRef := parent.RefKey
	if parentRef == "" {
		var pErr error
		if parentRef, pErr = computeTaskRefKey(db, parent); pErr != nil {
			return "", pErr
		}
	}
	// Next sibling index = max index used by existing siblings + 1, so keys
	// never collide even after siblings are deleted.
	var siblings []Task
	if err := db.
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

// UpdateTaskFields writes only the named columns of a task and returns the
// reloaded row. Callers that hold a copy of the task loaded earlier (an HTTP
// handler, a lifecycle hook) must use this rather than saving that copy: a
// full-row save would write back every column as it was when the copy was
// read, silently undoing whatever the engine changed in between. done_at
// follows a status change exactly as SetTaskStatusIf does.
func (q *TaskRepository) UpdateTaskFields(ctx context.Context, taskID int32, fields map[string]interface{}) (Task, error) {
	if len(fields) > 0 {
		err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			updates := make(map[string]interface{}, len(fields)+1)
			for column, value := range fields {
				updates[column] = value
			}
			if status, ok := fields["status"].(string); ok {
				var previous Task
				if err := tx.Select("id", "status", "done_at").First(&previous, taskID).Error; err != nil {
					return err
				}
				switch {
				case status != TaskStatusDone:
					updates["done_at"] = nil
				case previous.Status != TaskStatusDone || previous.DoneAt == nil:
					updates["done_at"] = time.Now()
				}
			}
			if err := tx.Model(&Task{}).Where("id = ?", taskID).Updates(updates).Error; err != nil {
				return err
			}
			if _, reparented := fields["parent_id"]; reparented {
				return recomputeTaskTree(tx, taskID)
			}
			return nil
		})
		if err != nil {
			return Task{}, err
		}
	}
	return q.GetTask(ctx, taskID)
}

// recomputeTaskTree restores root_task_id and depth across the company of the
// given task after it moved to another parent: the task and everything below
// it now sit in a different tree. Re-parenting is a rare manual edit, so the
// whole company is recomputed rather than tracking the affected subtree.
func recomputeTaskTree(db *gorm.DB, taskID int32) error {
	var task Task
	if err := db.Select("id", "company_id").First(&task, taskID).Error; err != nil {
		return err
	}
	return db.Exec(`WITH RECURSIVE tree(id, root_id, depth) AS (
  SELECT id, id, 0 FROM tasks WHERE parent_id IS NULL AND company_id = ?
  UNION ALL
  SELECT child.id, tree.root_id, tree.depth + 1 FROM tasks AS child JOIN tree ON child.parent_id = tree.id
)
UPDATE tasks SET
  root_task_id = COALESCE((SELECT root_id FROM tree WHERE tree.id = tasks.id), tasks.id),
  depth = COALESCE((SELECT depth FROM tree WHERE tree.id = tasks.id), 0)
WHERE company_id = ?`, task.CompanyID, task.CompanyID).Error
}

func (q *TaskRepository) GetTask(ctx context.Context, id int32) (Task, error) {
	return getTask(q.db.WithContext(ctx), id)
}

func getTask(db *gorm.DB, id int32) (Task, error) {
	var t Task
	err := db.Preload("Company").Preload("Project").Preload("Sprint").First(&t, id).Error
	return t, err
}

// GetRootTask walks the parent chain from taskID and returns the top-most
// ancestor (the task itself when it has no parent). Bounded to 20 hops to
// guard against cycles.
func (q *TaskRepository) GetRootTask(ctx context.Context, taskID int32) (Task, error) {
	return getRootTask(q.db.WithContext(ctx), taskID)
}

func getRootTask(db *gorm.DB, taskID int32) (Task, error) {
	task, err := getTask(db, taskID)
	if err != nil {
		return Task{}, err
	}
	for hops := 0; task.ParentID != nil && hops < 20; hops++ {
		parent, err := getTask(db, *task.ParentID)
		if err != nil {
			return task, nil // parent missing — treat current as root
		}
		task = parent
	}
	return task, nil
}

func (q *TaskRepository) ListAllTasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).Order("id").Find(&tasks).Error
	return tasks, err
}
