package models

import "time"

type ProviderPreset struct {
	ID           int32     `json:"id" gorm:"primaryKey"`
	Key          string    `json:"key" gorm:"uniqueIndex;not null"`
	Name         string    `json:"name" gorm:"not null"`
	BaseUrl      string    `json:"base_url" gorm:"not null"`
	ProviderType string    `json:"provider_type" gorm:"not null"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
