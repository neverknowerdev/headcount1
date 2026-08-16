package models

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

func ProviderSlug(provider LLMProvider) string {
	switch {
	case provider.ProviderName != "":
		return "builtin:" + slugifyProvider(provider.ProviderName)
	case provider.PresetKey != "":
		return "preset:" + slugifyProvider(provider.PresetKey)
	default:
		return "custom:" + slugifyProvider(provider.Name) + ":" + slugifyProvider(provider.BaseUrl)
	}
}

func (provider *LLMProvider) BeforeCreate(_ *gorm.DB) error {
	if provider.Slug == "" {
		provider.Slug = ProviderSlug(*provider)
	}
	return nil
}

func slugifyProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	previousDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
			previousDash = false
		} else if !previousDash {
			result.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

type LLMProvider struct {
	ID              int32     `json:"id" gorm:"primaryKey"`
	Name            string    `json:"name" gorm:"not null"`
	BaseUrl         string    `json:"base_url" gorm:"not null"`
	ApiKeyEncrypted string    `json:"-" gorm:"column:api_key;not null;serializer:sealed"`
	HasApiKey       bool      `json:"has_api_key" gorm:"-"`
	UserID          *int32    `json:"user_id" gorm:"index"`
	ProviderType    string    `json:"provider_type"`
	DefaultModel    string    `json:"default_model"`
	SupportedModels string    `json:"supported_models"`
	Builtin         bool      `json:"builtin" gorm:"not null;default:false"`
	Enabled         bool      `json:"enabled" gorm:"not null;default:true"`
	PresetKey       string    `json:"preset_key" gorm:"default:''"`
	ProviderName    string    `json:"provider_name" gorm:"default:''"`
	Slug            string    `json:"slug" gorm:"index;default:''"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ApiKeyEncrypted holds the SEALED api key (ciphertext) — both at rest and in
// memory. It is never serialized to clients (the frontend only sees
// HasApiKey). Decrypt at the point of use via secrets.Default().Decrypt().
// Slug is the provider's stable, portable domain identity — deterministically
// derived from the fields that make one provider "the same" as another
// (builtin vendor name, preset key, or name+base URL). Unlike the DB id it
// survives an export/import into a different database, so tenant restore can
// dedup providers by slug instead of duplicating them. Set by BeforeCreate
// and backfilled for existing rows (BackfillProviderSlugs); deterministic so
// two independently-seeded accounts share a slug for the same builtin.
