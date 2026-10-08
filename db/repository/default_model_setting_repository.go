package repository

import (
	"context"

	. "agent-orchestrator/db/models"
	"gorm.io/gorm"
)

type DefaultModelSettingRepository struct{ db *gorm.DB }

func NewDefaultModelSettingRepository(db *gorm.DB) *DefaultModelSettingRepository {
	return &DefaultModelSettingRepository{db: db}
}

// The Default Models slots. Smart models decide, cheap models execute, and
// the optional classifier answers typed yes/no and choice questions that gate
// the other two; commit messages have a slot of their own.
const (
	PurposeCommitMessages = "commit_messages"
	PurposeSmart          = "smart"
	PurposeCheap          = "cheap"
	PurposeClassifier     = "classifier"
)

var defaultModelSettingPurposes = []string{PurposeSmart, PurposeCheap, PurposeClassifier, PurposeCommitMessages}

func (q *DefaultModelSettingRepository) GetDefaultModelSetting(ctx context.Context, userID int32, purpose string) (DefaultModelSetting, error) {
	var s DefaultModelSetting
	err := q.db.WithContext(ctx).Where("user_id = ? AND purpose = ?", userID, purpose).First(&s).Error
	return s, err
}

func (q *DefaultModelSettingRepository) ListDefaultModelSettings(ctx context.Context, userID int32) ([]DefaultModelSetting, error) {
	var list []DefaultModelSetting
	err := q.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Preload("Provider").
		Preload("ModelGroup").
		Preload("ModelGroup.Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("ModelGroup.Members.Provider").
		Order("purpose").
		Find(&list).Error
	return list, err
}

// UpdateDefaultModelSetting overwrites a slot's target. providerID and
// modelGroupID are expected to be mutually exclusive (the caller — the API
// handler — enforces that); passing both nil leaves the slot unconfigured.
func (q *DefaultModelSettingRepository) UpdateDefaultModelSetting(ctx context.Context, userID int32, purpose string, providerID *int32, model string, modelGroupID *int32) (DefaultModelSetting, error) {
	var s DefaultModelSetting
	if err := q.db.WithContext(ctx).Where("user_id = ? AND purpose = ?", userID, purpose).First(&s).Error; err != nil {
		return DefaultModelSetting{}, err
	}
	s.ProviderID = providerID
	s.Model = model
	s.ModelGroupID = modelGroupID
	if err := q.db.WithContext(ctx).Select("provider_id", "model", "model_group_id").Updates(&s).Error; err != nil {
		return DefaultModelSetting{}, err
	}
	return s, nil
}

// EnsureDefaultModelSettingsForUser seeds the user's unconfigured row per
// slot if missing, so every slot shows up to be configured. Idempotent: it
// never overwrites an existing row's configuration.
func (q *DefaultModelSettingRepository) EnsureDefaultModelSettingsForUser(ctx context.Context, userID int32) error {
	uid := userID
	for _, purpose := range defaultModelSettingPurposes {
		var existing DefaultModelSetting
		err := q.db.WithContext(ctx).Where("user_id = ? AND purpose = ?", userID, purpose).First(&existing).Error
		if err == nil {
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if err := q.db.WithContext(ctx).Create(&DefaultModelSetting{Purpose: purpose, UserID: &uid}).Error; err != nil {
			return err
		}
	}
	return nil
}
