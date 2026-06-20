package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// LoadFromFile parses an AgentConfig from the TOML file at path.
// If the config specifies a PromptFile, its contents are loaded and stored
// in Prompt (relative paths are resolved from the directory of path).
func LoadFromFile(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	return LoadFromBytes(data, filepath.Dir(path))
}

// LoadFromBytes parses an AgentConfig from raw TOML bytes.
// baseDir is used to resolve relative PromptFile paths; pass "" to skip.
func LoadFromBytes(data []byte, baseDir string) (*AgentConfig, error) {
	var cfg AgentConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent config TOML: %w", err)
	}
	if cfg.PromptFile != "" && cfg.Prompt == "" {
		promptPath := cfg.PromptFile
		if !filepath.IsAbs(promptPath) && baseDir != "" {
			promptPath = filepath.Join(baseDir, promptPath)
		}
		promptData, err := os.ReadFile(promptPath)
		if err != nil {
			return nil, fmt.Errorf("load prompt file %s: %w", promptPath, err)
		}
		cfg.Prompt = string(promptData)
	}
	return &cfg, nil
}
