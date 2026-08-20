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
		{"empty list allows all", nil, "read", true},
		{"explicit match", []string{"read", "write"}, "read", true},
		{"explicit no match", []string{"read"}, "bash", false},
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
allowed_tools = ["read", "grep"]
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
	assert.Equal(t, []string{"read", "grep"}, cfg.AllowedTools)
	assert.Equal(t, []string{"Other"}, cfg.Subagents)
	assert.Equal(t, "Boss", cfg.ParentAgent)
}

func TestLoadFromBytes_InvalidTOML(t *testing.T) {
	_, err := agentconfig.LoadFromBytes([]byte("not : valid : toml :::"), "")
	require.Error(t, err)
}

func TestLoadYAMLFromBytes_Valid(t *testing.T) {
	cfg, err := agentconfig.LoadYAMLFromBytes([]byte(`
name: YAML Agent
short_name: YAML
description: yaml description
chat_type: message_history
reasoning_level: medium
allowed_tools: [read, grep]
subagents: [QA]
`), "")
	require.NoError(t, err)
	assert.Equal(t, "YAML Agent", cfg.Name)
	assert.Equal(t, "YAML", cfg.ShortName)
	assert.Equal(t, []string{"read", "grep"}, cfg.AllowedTools)
	assert.Equal(t, []string{"QA"}, cfg.Subagents)
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
	expected := []string{"CEO", "CTO", "Coder", "QA Lead", "QA Manual", "QA", "Debugger", "UX Designer", "Graphic Designer", "CMO", "SMM", "Writer", "Ads manager"}
	for _, name := range expected {
		assert.Contains(t, names, name, "builtin agent %q should be registered", name)
	}
}

func TestBuiltinConfigs_PreserveFilenameOrder(t *testing.T) {
	expected := []string{"CEO", "CTO", "Coder", "QA Lead", "QA Manual", "QA", "Debugger", "UX Designer", "Graphic Designer", "CMO", "SMM", "Writer", "Ads manager"}
	configs := agentconfig.BuiltinConfigs()
	require.Len(t, configs, len(expected))
	for i, cfg := range configs {
		assert.Equal(t, expected[i], cfg.Name, "built-in config order at index %d", i)
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
	// Builtin configs intentionally have no hardcoded models so that the
	// runtime resolver picks from the configured provider's supported list.
	assert.Empty(t, cfg.AllowedModels)

	cfg, err = f.GetConfig("Coder")
	require.NoError(t, err)
	assert.Equal(t, agentconfig.ChatTypeMessageHistory, cfg.ChatType)
	assert.Empty(t, cfg.AllowedModels)
	for _, tool := range []string{"bash", "read", "write", "ls", "grep"} {
		assert.True(t, cfg.IsToolAllowed(tool), "Coder should be allowed to use runtime tool %q", tool)
	}
	for _, legacy := range []string{"exec_command", "read_file", "write_file", "list_dir"} {
		assert.False(t, cfg.IsToolAllowed(legacy), "legacy tool name %q must not be used in the runtime allowlist", legacy)
	}
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
	for _, name := range []string{"CEO", "CTO", "Coder", "QA Lead", "QA Manual", "QA", "Debugger", "UX Designer", "Graphic Designer", "CMO", "SMM", "Writer", "Ads manager"} {
		cfg, err := f.GetConfig(name)
		require.NoError(t, err)
		assert.NotEmpty(t, cfg.Prompt, "agent %q should have a non-empty prompt", name)
	}
}

func TestDefaultFactory_SubagentHierarchy(t *testing.T) {
	f := agentconfig.NewDefaultFactory()

	ceo, _ := f.GetConfig("CEO")
	assert.Contains(t, ceo.Subagents, "CTO")
	assert.Contains(t, ceo.Subagents, "CMO")
	assert.Contains(t, ceo.Subagents, "UX Designer")
	assert.Contains(t, ceo.Subagents, "Graphic Designer")

	cto, _ := f.GetConfig("CTO")
	assert.Equal(t, "CEO", cto.ParentAgent)
	assert.Contains(t, cto.Subagents, "Coder")
	assert.Contains(t, cto.Subagents, "Debugger")
	assert.Contains(t, cto.Subagents, "QA Lead")
	assert.Contains(t, cto.Subagents, "QA Manual")
	assert.Contains(t, cto.Subagents, "QA")
	assert.Contains(t, cto.Subagents, "Debugger")

	cmo, _ := f.GetConfig("CMO")
	assert.Equal(t, "CEO", cmo.ParentAgent)
	assert.Contains(t, cmo.Subagents, "SMM")
	assert.Contains(t, cmo.Subagents, "Ads manager")
	assert.Contains(t, cmo.Subagents, "Writer")

	coder, _ := f.GetConfig("Coder")
	assert.Equal(t, "CTO", coder.ParentAgent)
}
