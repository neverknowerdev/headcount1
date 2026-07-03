package db

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// ReplaceSpecItems replaces all not-yet-verified spec items of the given kind
// on a task with a fresh pending set defined by runID. Verified items
// (passed/failed) are preserved so re-refinement cannot erase audit history.
func (q *Queries) ReplaceSpecItems(ctx context.Context, taskID int32, kind string, texts []string, runID int32) ([]SpecItem, error) {
	var created []SpecItem
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ? AND kind = ? AND status = ?", taskID, kind, "pending").
			Delete(&SpecItem{}).Error; err != nil {
			return err
		}
		for _, text := range texts {
			item := SpecItem{
				TaskID:         taskID,
				Kind:           kind,
				Text:           text,
				Status:         "pending",
				DefinedByRunID: runID,
			}
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
			created = append(created, item)
		}
		return nil
	})
	return created, err
}

// ListSpecItemsByTask returns all spec items for a task, criteria first,
// ordered by creation time.
func (q *Queries) ListSpecItemsByTask(ctx context.Context, taskID int32) ([]SpecItem, error) {
	var items []SpecItem
	err := q.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("kind asc, id asc").
		Find(&items).Error
	return items, err
}

// GetSpecItem loads a single spec item by ID.
func (q *Queries) GetSpecItem(ctx context.Context, id int32) (SpecItem, error) {
	var item SpecItem
	err := q.db.WithContext(ctx).First(&item, id).Error
	return item, err
}

// UpdateSpecItemVerdict records a verification verdict for an item.
// The verifying run must differ from the run that defined the item.
func (q *Queries) UpdateSpecItemVerdict(ctx context.Context, id int32, status, note string, verifierRunID int32) error {
	if status != "passed" && status != "failed" {
		return fmt.Errorf("invalid verdict status %q (must be passed or failed)", status)
	}
	var item SpecItem
	if err := q.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return err
	}
	if item.DefinedByRunID == verifierRunID {
		return fmt.Errorf("spec item %d was defined by this same run; verification must be done by an independent run (delegate it, e.g. to QA)", id)
	}
	return q.db.WithContext(ctx).Model(&SpecItem{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": status, "note": note, "verified_by_run_id": verifierRunID}).Error
}

// CountPendingSpecItems returns how many spec items on a task are still pending.
func (q *Queries) CountPendingSpecItems(ctx context.Context, taskID int32) (int64, error) {
	var count int64
	err := q.db.WithContext(ctx).Model(&SpecItem{}).
		Where("task_id = ? AND status = ?", taskID, "pending").
		Count(&count).Error
	return count, err
}
