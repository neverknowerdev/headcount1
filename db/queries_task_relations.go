package db

import (
	"context"
	"fmt"
	"sort"

	"gorm.io/gorm"
)

// TaskRelationTask is the compact task projection used by relation APIs and
// board summaries. It deliberately omits full task associations.
type TaskRelationTask struct {
	ID     int32  `json:"id"`
	RefKey string `json:"ref_key"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type TaskRelationView struct {
	RelationID int32            `json:"relation_id"`
	Task       TaskRelationTask `json:"task"`
}

type TaskRelationSummary struct {
	DependsOn []TaskRelationTask `json:"depends_on"`
	BlockedBy []TaskRelationTask `json:"blocked_by"`
	Blocks    []TaskRelationTask `json:"blocks"`
	RelatedTo []TaskRelationTask `json:"related_to"`
}

func (q *Queries) CreateTaskRelation(ctx context.Context, relation TaskRelation) (TaskRelation, error) {
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source, target Task
		if err := tx.First(&source, relation.SourceTaskID).Error; err != nil {
			return fmt.Errorf("source task %d: %w", relation.SourceTaskID, err)
		}
		if err := tx.First(&target, relation.TargetTaskID).Error; err != nil {
			return fmt.Errorf("target task %d: %w", relation.TargetTaskID, err)
		}
		if source.ID == target.ID {
			return fmt.Errorf("a task cannot relate to itself")
		}
		if source.CompanyID != target.CompanyID {
			return fmt.Errorf("tasks belong to different companies")
		}
		if relation.Kind != TaskRelationDependsOn && relation.Kind != TaskRelationRelatedTo {
			return fmt.Errorf("unsupported task relation kind %q", relation.Kind)
		}
		if relation.Kind == TaskRelationDependsOn && source.RunID != nil {
			return fmt.Errorf("cannot add a dependency to task %d while it has an active run", source.ID)
		}
		if relation.Kind == TaskRelationRelatedTo && relation.SourceTaskID > relation.TargetTaskID {
			relation.SourceTaskID, relation.TargetTaskID = relation.TargetTaskID, relation.SourceTaskID
		}
		relation.CompanyID = source.CompanyID
		if relation.Kind == TaskRelationDependsOn {
			ancestor, err := taskIsAncestor(tx, source.ID, target.ID)
			if err != nil {
				return err
			}
			if !ancestor {
				ancestor, err = taskIsAncestor(tx, target.ID, source.ID)
				if err != nil {
					return err
				}
			}
			if ancestor {
				return fmt.Errorf("hierarchy tasks cannot depend on one another")
			}
			cycle, err := dependencyPathExists(tx, target.ID, source.ID)
			if err != nil {
				return err
			}
			if cycle {
				return fmt.Errorf("task dependency would create a cycle")
			}
		}
		var existing TaskRelation
		if err := tx.Where("source_task_id = ? AND target_task_id = ? AND kind = ?", relation.SourceTaskID, relation.TargetTaskID, relation.Kind).First(&existing).Error; err == nil {
			return fmt.Errorf("task relation already exists")
		} else if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err := tx.Create(&relation).Error; err != nil {
			return err
		}
		return nil
	})
	return relation, err
}

func (q *Queries) DeleteTaskRelation(ctx context.Context, relationID int32) error {
	return q.db.WithContext(ctx).Delete(&TaskRelation{}, relationID).Error
}

func (q *Queries) GetTaskRelation(ctx context.Context, relationID int32) (TaskRelation, error) {
	var relation TaskRelation
	err := q.db.WithContext(ctx).First(&relation, relationID).Error
	return relation, err
}

func (q *Queries) ListTaskRelations(ctx context.Context, taskID int32) ([]TaskRelation, error) {
	var relations []TaskRelation
	err := q.db.WithContext(ctx).
		Where("source_task_id = ? OR target_task_id = ?", taskID, taskID).
		Order("id asc").Find(&relations).Error
	return relations, err
}

func (q *Queries) ListBlockingDependencies(ctx context.Context, taskID int32) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).
		Joins("JOIN task_relations tr ON tr.target_task_id = tasks.id").
		Where("tr.source_task_id = ? AND tr.kind = ? AND tasks.status <> ?", taskID, TaskRelationDependsOn, TaskStatusDone).
		Order("tasks.id asc").Find(&tasks).Error
	return tasks, err
}

func (q *Queries) ListDependentTasks(ctx context.Context, prerequisiteTaskID int32) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).
		Joins("JOIN task_relations tr ON tr.source_task_id = tasks.id").
		Where("tr.target_task_id = ? AND tr.kind = ?", prerequisiteTaskID, TaskRelationDependsOn).
		Order("tasks.id asc").Find(&tasks).Error
	return tasks, err
}

// ListQueuedTasksForReconciliation returns tasks which were explicitly queued
// but do not currently have an active run. It is used at boot to recover a
// crash window between a status commit and launching the next task.
func (q *Queries) ListQueuedTasksForReconciliation(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := q.db.WithContext(ctx).
		Where("status IN ? AND run_id IS NULL", []string{TaskStatusTodo, TaskStatusInProgress, TaskStatusDependsOnTask}).
		Order("id asc").Find(&tasks).Error
	return tasks, err
}

// MigrateLegacyTaskStatuses folds the old planning/refinement status into the
// unified state machine. An active refinement run is in progress; an idle one
// needs human attention and becomes blocked.
func (q *Queries) MigrateLegacyTaskStatuses(ctx context.Context) error {
	var tasks []Task
	if err := q.db.WithContext(ctx).Where("status = ?", "refinement").Find(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		status := TaskStatusBlocked
		if task.RunID != nil {
			status = TaskStatusInProgress
		}
		if err := q.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND status = ?", task.ID, "refinement").Update("status", status).Error; err != nil {
			return err
		}
	}
	return nil
}

func (q *Queries) CanStartTask(ctx context.Context, taskID int32) (bool, []Task, error) {
	blockers, err := q.ListBlockingDependencies(ctx, taskID)
	if err != nil {
		return false, nil, err
	}
	return len(blockers) == 0, blockers, nil
}

// ListTaskRelationSummaries batch-loads every relation endpoint for a set of
// tasks. It avoids the N+1 query pattern on the board task list.
func (q *Queries) ListTaskRelationSummaries(ctx context.Context, taskIDs []int32) (map[int32]TaskRelationSummary, error) {
	result := make(map[int32]TaskRelationSummary, len(taskIDs))
	if len(taskIDs) == 0 {
		return result, nil
	}
	unique := make(map[int32]struct{}, len(taskIDs))
	for _, id := range taskIDs {
		if id != 0 {
			unique[id] = struct{}{}
			result[id] = TaskRelationSummary{}
		}
	}
	ids := make([]int32, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	var relations []TaskRelation
	if err := q.db.WithContext(ctx).
		Where("(source_task_id IN ? OR target_task_id IN ?) AND kind IN ?", ids, ids, []string{TaskRelationDependsOn, TaskRelationRelatedTo}).
		Order("id asc").Find(&relations).Error; err != nil {
		return nil, err
	}

	endpointIDs := make(map[int32]struct{}, len(relations)*2)
	for _, relation := range relations {
		endpointIDs[relation.SourceTaskID] = struct{}{}
		endpointIDs[relation.TargetTaskID] = struct{}{}
	}
	lookupIDs := make([]int32, 0, len(endpointIDs))
	for id := range endpointIDs {
		lookupIDs = append(lookupIDs, id)
	}
	var tasks []Task
	if len(lookupIDs) > 0 {
		if err := q.db.WithContext(ctx).Where("id IN ?", lookupIDs).Find(&tasks).Error; err != nil {
			return nil, err
		}
	}
	byID := make(map[int32]TaskRelationTask, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = TaskRelationTask{ID: task.ID, RefKey: task.RefKey, Title: task.Title, Status: task.Status}
	}
	requested := make(map[int32]struct{}, len(ids))
	for _, id := range ids {
		requested[id] = struct{}{}
	}
	for _, relation := range relations {
		source, sourceOK := byID[relation.SourceTaskID]
		target, targetOK := byID[relation.TargetTaskID]
		if relation.Kind == TaskRelationDependsOn {
			if _, ok := requested[relation.SourceTaskID]; ok && targetOK {
				summary := result[relation.SourceTaskID]
				summary.DependsOn = append(summary.DependsOn, target)
				if target.Status != TaskStatusDone {
					summary.BlockedBy = append(summary.BlockedBy, target)
				}
				result[relation.SourceTaskID] = summary
			}
			if _, ok := requested[relation.TargetTaskID]; ok && sourceOK {
				summary := result[relation.TargetTaskID]
				summary.Blocks = append(summary.Blocks, source)
				result[relation.TargetTaskID] = summary
			}
			continue
		}
		if relation.Kind == TaskRelationRelatedTo {
			if _, ok := requested[relation.SourceTaskID]; ok && targetOK {
				summary := result[relation.SourceTaskID]
				summary.RelatedTo = append(summary.RelatedTo, target)
				result[relation.SourceTaskID] = summary
			}
			if _, ok := requested[relation.TargetTaskID]; ok && sourceOK {
				summary := result[relation.TargetTaskID]
				summary.RelatedTo = append(summary.RelatedTo, source)
				result[relation.TargetTaskID] = summary
			}
		}
	}
	for id, summary := range result {
		sort.Slice(summary.DependsOn, func(i, j int) bool { return summary.DependsOn[i].ID < summary.DependsOn[j].ID })
		sort.Slice(summary.BlockedBy, func(i, j int) bool { return summary.BlockedBy[i].ID < summary.BlockedBy[j].ID })
		sort.Slice(summary.Blocks, func(i, j int) bool { return summary.Blocks[i].ID < summary.Blocks[j].ID })
		sort.Slice(summary.RelatedTo, func(i, j int) bool { return summary.RelatedTo[i].ID < summary.RelatedTo[j].ID })
		result[id] = summary
	}
	return result, nil
}

func taskIsAncestor(tx *gorm.DB, firstID, secondID int32) (bool, error) {
	current := secondID
	for hops := 0; current != 0 && hops < 100; hops++ {
		var task Task
		if err := tx.Select("id", "parent_id").First(&task, current).Error; err != nil {
			return false, err
		}
		if task.ParentID == nil {
			return false, nil
		}
		if *task.ParentID == firstID {
			return true, nil
		}
		current = *task.ParentID
	}
	return false, nil
}

func dependencyPathExists(tx *gorm.DB, fromID, targetID int32) (bool, error) {
	visited := map[int32]bool{}
	frontier := []int32{fromID}
	for len(frontier) > 0 {
		current := frontier[0]
		frontier = frontier[1:]
		if current == targetID {
			return true, nil
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		var next []int32
		if err := tx.Model(&TaskRelation{}).
			Where("source_task_id = ? AND kind = ?", current, TaskRelationDependsOn).
			Pluck("target_task_id", &next).Error; err != nil {
			return false, err
		}
		frontier = append(frontier, next...)
	}
	return false, nil
}
