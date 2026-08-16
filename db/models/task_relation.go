package models

import "time"

type TaskRelation struct {
	ID           int32     `json:"id" gorm:"primaryKey"`
	CompanyID    int32     `json:"company_id" gorm:"not null;index"`
	SourceTaskID int32     `json:"source_task_id" gorm:"not null;index;uniqueIndex:idx_task_relations_unique"`
	TargetTaskID int32     `json:"target_task_id" gorm:"not null;index;uniqueIndex:idx_task_relations_unique"`
	Kind         string    `json:"kind" gorm:"not null;index;uniqueIndex:idx_task_relations_unique"`
	CreatedAt    time.Time `json:"created_at"`
	SourceTask   Task      `json:"-" gorm:"foreignKey:SourceTaskID;constraint:OnDelete:CASCADE"`
	TargetTask   Task      `json:"-" gorm:"foreignKey:TargetTaskID;constraint:OnDelete:CASCADE"`
}
