package db

import (
	"context"
	"slices"
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

// Stable ProviderName values for the two builtin free providers — see
// LLMProvider.ProviderName's doc comment for why these exist separately
// from the (user-editable) display Name.
const (
	ProviderVendorOpenRouter  = "OpenRouter"
	ProviderVendorOpenCodeZen = "OpenCode"
)

func (q *Queries) CreateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	p.HasApiKey = p.ApiKeyEncrypted != ""
	return p, err
}

func (q *Queries) GetLLMProvider(ctx context.Context, id int32) (LLMProvider, error) {
	var p LLMProvider
	err := q.db.WithContext(ctx).First(&p, id).Error
	p.HasApiKey = p.ApiKeyEncrypted != ""
	return p, err
}

func (q *Queries) ListLLMProviders(ctx context.Context) ([]LLMProvider, error) {
	var p []LLMProvider
	err := q.db.WithContext(ctx).Order("id").Find(&p).Error
	for i := range p {
		p[i].HasApiKey = p[i].ApiKeyEncrypted != ""
	}
	return p, err
}

// ListLLMProvidersForUser returns only the user's providers (builtins are
// seeded per user, so this includes their own OpenRouter/OpenCode rows).
func (q *Queries) ListLLMProvidersForUser(ctx context.Context, userID int32) ([]LLMProvider, error) {
	var p []LLMProvider
	err := q.db.WithContext(ctx).Where("user_id = ?", userID).Order("id").Find(&p).Error
	for i := range p {
		p[i].HasApiKey = p[i].ApiKeyEncrypted != ""
	}
	return p, err
}

func (q *Queries) DeleteLLMProvider(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&LLMProvider{}, id).Error
}

func (q *Queries) UpdateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error) {
	// ApiKeyEncrypted is ciphertext: a metadata-only edit round-trips the sealed
	// value verbatim (a locked read no longer blanks it), and a key change was
	// re-sealed by the caller. So a plain Save can never destroy the secret.
	err := q.db.WithContext(ctx).Save(&p).Error
	p.HasApiKey = p.ApiKeyEncrypted != ""
	return p, err
}

// EnsureBuiltinLLMProvidersForUser creates the user's own OpenRouter and
// OpenCode Zen builtin provider rows if missing (matched by provider_name).
// Builtin providers are per-user in cloud mode: each user activates them with
// their own (free) API key. Called at registration and at startup for every
// existing user (so upgrades that add new builtins reach everyone).
// Idempotent; never overwrites a row the user has customized.
func (q *Queries) EnsureBuiltinLLMProvidersForUser(ctx context.Context, userID int32) error {
	uid := userID
	predefined := []LLMProvider{
		{
			Name:         ProviderNameOpenRouter,
			BaseUrl:      OpenRouterBaseURL,
			ProviderType: "openai",
			Builtin:      true,
			Enabled:      true,
			ProviderName: ProviderVendorOpenRouter,
			UserID:       &uid,
		},
		{
			Name:         ProviderNameOpenCodeZen,
			BaseUrl:      OpenCodeZenBaseURL,
			ProviderType: "openai",
			Builtin:      true,
			Enabled:      true,
			ProviderName: ProviderVendorOpenCodeZen,
			UserID:       &uid,
		},
	}

	for _, p := range predefined {
		var existing LLMProvider
		if q.db.WithContext(ctx).
			Where("user_id = ? AND provider_name = ?", userID, p.ProviderName).
			First(&existing).Error == nil {
			continue
		}
		if err := q.db.WithContext(ctx).Create(&p).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpdateLLMProviderModelCatalog stores a freshly discovered list of model IDs
// on a provider. DefaultModel is only re-picked when it's currently empty or
// no longer present in the new catalog — e.g. the upstream provider removed
// or renamed the model — so a stale default (like one pointing at a model
// that no longer exists) can never survive a catalog refresh, while a
// still-valid default stays stable across refreshes even if the fetched
// order changes. A nil/empty models list is a no-op — a transient discovery
// failure should never blank out a previously known-good catalog.
func (q *Queries) UpdateLLMProviderModelCatalog(ctx context.Context, providerID int32, models []string) error {
	if len(models) == 0 {
		return nil
	}
	var existing LLMProvider
	if err := q.db.WithContext(ctx).First(&existing, providerID).Error; err != nil {
		return err
	}
	updates := map[string]any{"supported_models": strings.Join(models, ",")}
	if existing.DefaultModel == "" || !slices.Contains(models, existing.DefaultModel) {
		// models is ordered by the caller (e.g. pkg/llmdiscovery's
		// sortByPriority) with its preferred default first.
		updates["default_model"] = models[0]
	}
	return q.db.WithContext(ctx).Model(&existing).Updates(updates).Error
}

// ForceUpdateLLMProviderModelCatalog replaces a provider's model catalog and
// always re-picks DefaultModel from the fresh list, unlike
// UpdateLLMProviderModelCatalog which leaves a still-valid DefaultModel
// alone. Used for explicit, user-triggered re-discovery — getting the
// current best pick is exactly the point of that action.
func (q *Queries) ForceUpdateLLMProviderModelCatalog(ctx context.Context, providerID int32, models []string) error {
	if len(models) == 0 {
		return nil
	}
	return q.db.WithContext(ctx).Model(&LLMProvider{}).Where("id = ?", providerID).Updates(map[string]any{
		"supported_models": strings.Join(models, ","),
		"default_model":    models[0],
	}).Error
}
