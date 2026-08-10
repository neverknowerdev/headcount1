package engine

import (
	"testing"

	"agent-orchestrator/db"

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

func TestResolveModel_AgentModelWins(t *testing.T) {
	m, err := resolveModel(provider("default-m", "default-m,other-m"), "agent-m")
	require.NoError(t, err)
	assert.Equal(t, "agent-m", m)
}

func TestResolveModel_EmptyAgentModelUsesProviderDefault(t *testing.T) {
	m, err := resolveModel(provider("default-m", "default-m"), "")
	require.NoError(t, err)
	assert.Equal(t, "default-m", m)
}

func TestResolveModel_NoModelAnywhereErrors(t *testing.T) {
	_, err := resolveModel(provider("", ""), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no default model")
}
