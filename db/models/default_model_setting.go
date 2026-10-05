package models

import "time"

type DefaultModelSetting struct {
	ID           int32        `json:"id" gorm:"primaryKey"`
	Purpose      string       `json:"purpose" gorm:"not null;uniqueIndex:idx_dms_user_purpose"`
	UserID       *int32       `json:"user_id" gorm:"uniqueIndex:idx_dms_user_purpose"`
	ProviderID   *int32       `json:"provider_id"`
	Provider     *LLMProvider `json:"provider,omitempty" gorm:"foreignKey:ProviderID;constraint:OnDelete:SET NULL;"`
	Model        string       `json:"model"`
	ModelGroupID *int32       `json:"model_group_id"`
	ModelGroup   *ModelGroup  `json:"model_group,omitempty" gorm:"foreignKey:ModelGroupID;constraint:OnDelete:SET NULL;"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// owning user
