package models

import "time"

type Skill struct {
	ID          int32     `json:"id" gorm:"primaryKey"`
	CompanyID   int32     `json:"company_id" gorm:"not null"`
	Company     Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description"`
	SourceUrl   string    `json:"source_url"`
	LocalPath   string    `json:"local_path" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
