package models

import "time"

type Artifact struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	CompanyID   *int32    `json:"company_id" gorm:"index"`
	Company     *Company  `json:"company,omitempty" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID   *int32    `json:"project_id" gorm:"index"`
	Project     *Project  `json:"project,omitempty" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	TaskID      int32     `json:"task_id" gorm:"not null"`
	Task        Task      `json:"task" gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE;"`
	RunID       int32     `json:"run_id" gorm:"not null"`
	Run         Run       `json:"run" gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE;"`
	Filename    string    `json:"filename" gorm:"not null"`
	FilePath    string    `json:"file_path" gorm:"not null"`
	Content     string    `json:"content" gorm:"type:text"`
	Description string    `json:"description" gorm:"type:text;default:''"`
	IsVerified  bool      `json:"is_verified" gorm:"not null;default:false"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Description is a one-line summary provided by the producing agent.
// IsVerified is set when a QA verification session passes all spec items
// for the artifact's task.
