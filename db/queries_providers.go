package db

import (
	"context"
	"strings"
)

// Names and base URLs of the free-model providers seeded automatically on
// startup. Exported so pkg/llmdiscovery (which performs the live model
// catalog fetch) can target the same rows without hardcoding them twice.
const (
	ProviderNameOpenRouter  = "OpenRouter Free Models"
	ProviderNameOpenCodeZen = "OpenCode Free Models"

	OpenRouterBaseURL  = "https://openrouter.ai/api/v1"
	OpenCodeZenBaseURL = "https://opencode.ai/zen/v1"
)

func (q *Queries) CreateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *Queries) GetLLMProvider(ctx context.Context, id int32) (LLMProvider, error) {
	var p LLMProvider
	err := q.db.WithContext(ctx).First(&p, id).Error
	return p, err
}

func (q *Queries) ListLLMProviders(ctx context.Context) ([]LLMProvider, error) {
	var p []LLMProvider
	err := q.db.WithContext(ctx).Order("id").Find(&p).Error
	return p, err
}

func (q *Queries) DeleteLLMProvider(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&LLMProvider{}, id).Error
}

func (q *Queries) UpdateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error) {
	err := q.db.WithContext(ctx).Save(&p).Error
	return p, err
}

// EnsureBuiltinLLMProviders creates the OpenRouter and OpenCode Zen builtin
// providers if they don't already exist (matched by name). Both are
// OpenAI-compatible gateways offering free models, so they're seeded with an
// empty API key — the user only needs to paste in a (free) API key to start
// using them. Safe to call on every startup; never overwrites a row a user
// has already customized, beyond flipping Builtin on if it was somehow unset.
func (q *Queries) EnsureBuiltinLLMProviders(ctx context.Context) error {
	predefined := []LLMProvider{
		{
			Name:         ProviderNameOpenRouter,
			BaseUrl:      OpenRouterBaseURL,
			ProviderType: "openai",
			Builtin:      true,
		},
		{
			Name:         ProviderNameOpenCodeZen,
			BaseUrl:      OpenCodeZenBaseURL,
			ProviderType: "openai",
			Builtin:      true,
		},
	}

	for _, p := range predefined {
		var existing LLMProvider
		if q.db.WithContext(ctx).Where("name = ?", p.Name).First(&existing).Error == nil {
			if !existing.Builtin {
				q.db.WithContext(ctx).Model(&existing).Update("builtin", true)
			}
			continue
		}
		if err := q.db.WithContext(ctx).Create(&p).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpdateLLMProviderModelCatalog stores a freshly discovered list of model IDs
// on a provider. DefaultModel is only set when currently empty, so a
// background refresh never overrides a default the user picked deliberately.
// A nil/empty models list is a no-op — a transient discovery failure should
// never blank out a previously known-good catalog.
func (q *Queries) UpdateLLMProviderModelCatalog(ctx context.Context, providerID int32, models []string) error {
	if len(models) == 0 {
		return nil
	}
	var existing LLMProvider
	if err := q.db.WithContext(ctx).First(&existing, providerID).Error; err != nil {
		return err
	}
	updates := map[string]any{"supported_models": strings.Join(models, ",")}
	if existing.DefaultModel == "" {
		updates["default_model"] = models[0]
	}
	return q.db.WithContext(ctx).Model(&existing).Updates(updates).Error
}

// ForceUpdateLLMProviderModelCatalog replaces a provider's model catalog and
// always sets DefaultModel to the top of the freshly ranked list, unlike
// UpdateLLMProviderModelCatalog which never overwrites an existing
// DefaultModel. Used for explicit, user-triggered re-discovery — getting the
// current best-ranked pick is exactly the point of that action.
func (q *Queries) ForceUpdateLLMProviderModelCatalog(ctx context.Context, providerID int32, models []string) error {
	if len(models) == 0 {
		return nil
	}
	return q.db.WithContext(ctx).Model(&LLMProvider{}).Where("id = ?", providerID).Updates(map[string]any{
		"supported_models": strings.Join(models, ","),
		"default_model":    models[0],
	}).Error
}
