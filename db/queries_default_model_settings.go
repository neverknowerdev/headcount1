package db

import (
	"context"

	"gorm.io/gorm"
)

// Purposes with a configurable default model. Add a new one here (and seed
// a sensible zero-value default in EnsureDefaultModelSettings) whenever a
// new internal one-shot LLM use case is added.
const (
	PurposeCommitMessages = "commit_messages"
	PurposeAskArtifact    = "ask_artifact"
)

var defaultModelSettingPurposes = []string{PurposeCommitMessages, PurposeAskArtifact}

func (q *Queries) GetDefaultModelSetting(ctx context.Context, purpose string) (DefaultModelSetting, error) {
	var s DefaultModelSetting
	err := q.db.WithContext(ctx).Where("purpose = ?", purpose).First(&s).Error
	return s, err
}

func (q *Queries) ListDefaultModelSettings(ctx context.Context) ([]DefaultModelSetting, error) {
	var list []DefaultModelSetting
	err := q.db.WithContext(ctx).
		Preload("Provider").
		Preload("ModelGroup").
		Preload("ModelGroup.Members", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).
		Preload("ModelGroup.Members.Provider").
		Order("purpose").
		Find(&list).Error
	return list, err
}

// UpdateDefaultModelSetting overwrites a purpose's target. providerID and
// modelGroupID are expected to be mutually exclusive (the caller — the API
// handler — enforces that); passing both nil clears the override, falling
// back to the calling session's own LLM.
func (q *Queries) UpdateDefaultModelSetting(ctx context.Context, purpose string, providerID *int32, model string, modelGroupID *int32) (DefaultModelSetting, error) {
	var s DefaultModelSetting
	if err := q.db.WithContext(ctx).Where("purpose = ?", purpose).First(&s).Error; err != nil {
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

// EnsureDefaultModelSettings seeds an unconfigured row per known purpose if
// missing, so each purpose falls back to the calling session's own LLM
// until a user points it at a provider/model or a model group. Idempotent:
// never overwrites an existing row's configuration.
func (q *Queries) EnsureDefaultModelSettings(ctx context.Context) error {
	for _, purpose := range defaultModelSettingPurposes {
		var existing DefaultModelSetting
		err := q.db.WithContext(ctx).Where("purpose = ?", purpose).First(&existing).Error
		if err == nil {
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if err := q.db.WithContext(ctx).Create(&DefaultModelSetting{Purpose: purpose}).Error; err != nil {
			return err
		}
	}
	return nil
}
