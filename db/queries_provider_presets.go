package db

import "context"

// Keys of the built-in provider presets. Exported so pkg/llmdiscovery can
// register bespoke PresetDiscoverers against them without hardcoding the
// string twice.
const (
	ProviderPresetOpenCodeGo = "opencode-go"
	ProviderPresetMiniMax    = "minimax"
	ProviderPresetDeepSeek   = "deepseek"
)

func (q *Queries) ListProviderPresets(ctx context.Context) ([]ProviderPreset, error) {
	var p []ProviderPreset
	err := q.db.WithContext(ctx).Order("id").Find(&p).Error
	return p, err
}

func (q *Queries) GetProviderPresetByKey(ctx context.Context, key string) (ProviderPreset, error) {
	var p ProviderPreset
	err := q.db.WithContext(ctx).Where("key = ?", key).First(&p).Error
	return p, err
}

// EnsureProviderPresets creates the known provider presets if they don't
// already exist (matched by Key). Safe to call on every startup; never
// overwrites a row that already exists, so any future admin-side edits to a
// preset survive restarts.
func (q *Queries) EnsureProviderPresets(ctx context.Context) error {
	predefined := []ProviderPreset{
		{
			Key:          ProviderPresetOpenCodeGo,
			Name:         "OpenCode Go",
			BaseUrl:      "https://opencode.ai/zen/go/v1",
			ProviderType: "openai",
		},
		{
			Key:          ProviderPresetMiniMax,
			Name:         "MiniMax",
			BaseUrl:      "https://api.minimax.io/v1",
			ProviderType: "openai",
		},
		{
			Key:          ProviderPresetDeepSeek,
			Name:         "DeepSeek",
			BaseUrl:      "https://api.deepseek.com/v1",
			ProviderType: "openai",
		},
	}

	for _, p := range predefined {
		var existing ProviderPreset
		if q.db.WithContext(ctx).Where("key = ?", p.Key).First(&existing).Error == nil {
			continue
		}
		if err := q.db.WithContext(ctx).Create(&p).Error; err != nil {
			return err
		}
	}
	return nil
}
