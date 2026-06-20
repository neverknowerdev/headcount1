package agentconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/engine/agentconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- AgentConfig tests ------------------------------------------------------

func TestAgentConfig_DefaultModel(t *testing.T) {
	cfg := &agentconfig.AgentConfig{AllowedModels: []string{"model-a", "model-b"}}
	assert.Equal(t, "model-a", cfg.DefaultModel())

	empty := &agentconfig.AgentConfig{}
	assert.Equal(t, "", empty.DefaultModel())
}

func TestAgentConfig_IsToolAllowed(t *testing.T) {
	tests := []struct {
		name         string
		allowedTools []string
		tool         string
		want         bool
	}{
		{"empty list allows all", nil, "read_file", true},
		{"explicit match", []string{"read_file", "write_file"}, "read_file", true},
		{"explicit no match", []string{"read_file"}, "exec_command", false},
		{"wildcard allows all", []string{"*"}, "anything", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &agentconfig.AgentConfig{AllowedTools: tt.allowedTools}
			assert.Equal(t, tt.want, cfg.IsToolAllowed(tt.tool))
		})
	}
}

// ---- Loader tests -----------------------------------------------------------

const validTOML = `
name = "TestAgent"
description = "A test agent"
chat_type = "message_history"
allowed_models = ["model-x", "model-y"]
reasoning_level = "medium"
allowed_tools = ["read_file", "grep"]
subagents = ["Other"]
parent_agent = "Boss"
`

func TestLoadFromBytes_ValidTOML(t *testing.T) {
	cfg, err := agentconfig.LoadFromBytes([]byte(validTOML), "")
	require.NoError(t, err)
	assert.Equal(t, "TestAgent", cfg.Name)
	assert.Equal(t, "A test agent", cfg.Description)
	assert.Equal(t, agentconfig.ChatTypeMessageHistory, cfg.ChatType)
	assert.Equal(t, []string{"model-x", "model-y"}, cfg.AllowedModels)
	assert.Equal(t, agentconfig.ReasoningLevelMedium, cfg.ReasoningLevel)
	assert.Equal(t, []string{"read_file", "grep"}, cfg.AllowedTools)
	assert.Equal(t, []string{"Other"}, cfg.Subagents)
	assert.Equal(t, "Boss", cfg.ParentAgent)
}

func TestLoadFromBytes_InvalidTOML(t *testing.T) {
	_, err := agentconfig.LoadFromBytes([]byte("not : valid : toml :::"), "")
	require.Error(t, err)
}

func TestLoadFromFile_WithPromptFile(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "agent.md")
	require.NoError(t, os.WriteFile(promptPath, []byte("You are a test agent."), 0644))

	tomlContent := `
name = "FileAgent"
prompt_file = "agent.md"
chat_type = "compact_thinking"
allowed_models = ["model-z"]
reasoning_level = "max"
`
	cfgPath := filepath.Join(dir, "agent.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(tomlContent), 0644))

	cfg, err := agentconfig.LoadFromFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "FileAgent", cfg.Name)
	assert.Equal(t, "You are a test agent.", cfg.Prompt)
	assert.Equal(t, agentconfig.ChatTypeCompactThinking, cfg.ChatType)
	assert.Equal(t, agentconfig.ReasoningLevelMax, cfg.ReasoningLevel)
}

func TestLoadFromFile_MissingPromptFile(t *testing.T) {
	dir := t.TempDir()
	tomlContent := "name = \"X\"\nprompt_file = \"missing.md\"\n"
	cfgPath := filepath.Join(dir, "agent.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(tomlContent), 0644))

	_, err := agentconfig.LoadFromFile(cfgPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.md")
}

func TestLoadFromFile_NonExistentFile(t *testing.T) {
	_, err := agentconfig.LoadFromFile("/no/such/file.toml")
	require.Error(t, err)
}

// ---- Factory tests ----------------------------------------------------------

func TestDefaultFactory_BuiltinAgents(t *testing.T) {
	f := agentconfig.NewDefaultFactory()
	names := f.ListNames()
	expected := []string{"CEO", "CTO", "Programmer", "QA", "Writer", "Researcher"}
	for _, name := range expected {
		assert.Contains(t, names, name, "builtin agent %q should be registered", name)
	}
}

func TestDefaultFactory_GetConfig(t *testing.T) {
	f := agentconfig.NewDefaultFactory()

	cfg, err := f.GetConfig("CEO")
	require.NoError(t, err)
	assert.Equal(t, "CEO", cfg.Name)
	assert.NotEmpty(t, cfg.Prompt)
	assert.Equal(t, agentconfig.ChatTypeCompactThinking, cfg.ChatType)
	assert.Equal(t, agentconfig.ReasoningLevelMax, cfg.ReasoningLevel)

	cfg, err = f.GetConfig("Programmer")
	require.NoError(t, err)
	assert.Equal(t, agentconfig.ChatTypeMessageHistory, cfg.ChatType)
}

func TestDefaultFactory_GetConfig_NotFound(t *testing.T) {
	f := agentconfig.NewDefaultFactory()
	_, err := f.GetConfig("Unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unknown")
}

func TestDefaultFactory_Register_Override(t *testing.T) {
	f := agentconfig.NewDefaultFactory()
	custom := &agentconfig.AgentConfig{
		Name:   "CEO",
		Prompt: "custom prompt",
	}
	f.Register(custom)

	cfg, err := f.GetConfig("CEO")
	require.NoError(t, err)
	assert.Equal(t, "custom prompt", cfg.Prompt)
}

func TestEmptyFactory_Register(t *testing.T) {
	f := agentconfig.NewEmptyFactory()
	assert.Empty(t, f.ListNames())

	f.Register(&agentconfig.AgentConfig{Name: "MyAgent", Prompt: "hello"})
	names := f.ListNames()
	require.Len(t, names, 1)
	assert.Equal(t, "MyAgent", names[0])
}

func TestDefaultFactory_BuiltinPrompts_NotEmpty(t *testing.T) {
	f := agentconfig.NewDefaultFactory()
	for _, name := range []string{"CEO", "CTO", "Programmer", "QA", "Writer", "Researcher"} {
		cfg, err := f.GetConfig(name)
		require.NoError(t, err)
		assert.NotEmpty(t, cfg.Prompt, "agent %q should have a non-empty prompt", name)
	}
}

func TestDefaultFactory_SubagentHierarchy(t *testing.T) {
	f := agentconfig.NewDefaultFactory()

	ceo, _ := f.GetConfig("CEO")
	assert.Contains(t, ceo.Subagents, "CTO")

	cto, _ := f.GetConfig("CTO")
	assert.Equal(t, "CEO", cto.ParentAgent)
	assert.Contains(t, cto.Subagents, "Programmer")
	assert.Contains(t, cto.Subagents, "QA")

	prog, _ := f.GetConfig("Programmer")
	assert.Equal(t, "CTO", prog.ParentAgent)
}
