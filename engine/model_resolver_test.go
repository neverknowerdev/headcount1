package engine

import (
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSupportedModels(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"model-a", []string{"model-a"}},
		{"model-a,model-b,model-c", []string{"model-a", "model-b", "model-c"}},
		{" model-a , model-b ", []string{"model-a", "model-b"}},
		{",,model-a,,", []string{"model-a"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseSupportedModels(tt.input))
		})
	}
}

func provider(defaultModel, supportedModels string) db.LLMProvider {
	return db.LLMProvider{
		Name:            "test-provider",
		DefaultModel:    defaultModel,
		SupportedModels: supportedModels,
	}
}

func cfg(allowedModels ...string) *agentconfig.AgentConfig {
	return &agentconfig.AgentConfig{Name: "TestAgent", AllowedModels: allowedModels}
}

func TestResolveModel_NoCfg_UsesAgentModel(t *testing.T) {
	m, err := resolveModel(nil, provider("default-m", "default-m,other-m"), "agent-m")
	require.NoError(t, err)
	assert.Equal(t, "agent-m", m)
}

func TestResolveModel_NoCfg_NoAgentModel_UsesProviderDefault(t *testing.T) {
	m, err := resolveModel(nil, provider("default-m", "default-m"), "")
	require.NoError(t, err)
	assert.Equal(t, "default-m", m)
}

func TestResolveModel_NoCfg_NoModelAnywhere_Error(t *testing.T) {
	_, err := resolveModel(nil, provider("", ""), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default model")
}

func TestResolveModel_CfgAllowedModels_FirstMatchUsed(t *testing.T) {
	// AllowedModels: ["prefer-a", "prefer-b"], provider supports "prefer-b,other"
	// → should pick "prefer-b" (first that's available)
	m, err := resolveModel(cfg("prefer-a", "prefer-b"), provider("other", "prefer-b,other"), "")
	require.NoError(t, err)
	assert.Equal(t, "prefer-b", m)
}

func TestResolveModel_CfgAllowedModels_ExactFirstMatch(t *testing.T) {
	m, err := resolveModel(cfg("model-x", "model-y"), provider("model-y", "model-x,model-y"), "")
	require.NoError(t, err)
	assert.Equal(t, "model-x", m)
}

func TestResolveModel_CfgAllowedModels_NoneAvailable_Error(t *testing.T) {
	_, err := resolveModel(cfg("model-a", "model-b"), provider("other", "other,another"), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model-a")
	assert.Contains(t, err.Error(), "model-b")
	assert.Contains(t, err.Error(), "other")
}

func TestResolveModel_CfgAllowedModels_ProviderNoSupportedList_UsesDefault(t *testing.T) {
	// Provider has no SupportedModels list at all → skip validation, use default.
	m, err := resolveModel(cfg("model-a"), provider("default-m", ""), "")
	require.NoError(t, err)
	assert.Equal(t, "default-m", m)
}

func TestResolveModel_CfgAllowedModels_ProviderNoSupportedOrDefault_Error(t *testing.T) {
	_, err := resolveModel(cfg("model-a"), provider("", ""), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default model")
}

func TestResolveModel_EmptyCfgAllowedModels_FallsThrough(t *testing.T) {
	// AgentConfig with empty AllowedModels behaves as if no config.
	m, err := resolveModel(&agentconfig.AgentConfig{Name: "x", AllowedModels: nil}, provider("default-m", "default-m"), "")
	require.NoError(t, err)
	assert.Equal(t, "default-m", m)
}
