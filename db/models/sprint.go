package models

import "time"

type Sprint struct {
	ID        int32      `json:"id" gorm:"primaryKey"`
	CompanyID int32      `json:"company_id" gorm:"not null"`
	Company   Company    `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name      string     `json:"name" gorm:"not null"`
	Goal      string     `json:"goal"`
	StartDate *time.Time `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}
