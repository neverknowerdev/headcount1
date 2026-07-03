// Package agentconfig defines agent configuration types and a factory for
// creating and managing named agent configurations. Configs can be loaded from
// TOML files or constructed programmatically.
package agentconfig

import "strings"

// ReasoningLevel controls how much reasoning budget the LLM applies.
type ReasoningLevel string

const (
	ReasoningLevelLow    ReasoningLevel = "low"
	ReasoningLevelMedium ReasoningLevel = "medium"
	ReasoningLevelMax    ReasoningLevel = "max"
)

// ChatType controls how the agent manages its conversation state.
type ChatType string

const (
	// ChatTypeMessageHistory accumulates the full conversation history and
	// sends it on every LLM call (the default).
	ChatTypeMessageHistory ChatType = "message_history"

	// ChatTypeCompactThinking enables extended reasoning on every call.
	// The reasoning_effort is derived from ReasoningLevel.
	ChatTypeCompactThinking ChatType = "compact_thinking"
)

// AgentConfig defines the complete runtime configuration for an agent.
// It can be loaded from a TOML file or built in code.
type AgentConfig struct {
	// Name is the unique identifier used to look up this config.
	Name string `toml:"name"`
	// ShortName is a compact (≤7 chars) label used in run keys, e.g.
	// "DEC-50-CEO". Falls back to a name-derived abbreviation when empty.
	ShortName string `toml:"short_name"`
	// Description is a human-readable summary of the agent's role.
	Description string `toml:"description"`
	// Prompt is the system prompt text. Takes precedence over PromptFile.
	Prompt string `toml:"prompt"`
	// PromptFile is a path to a .md file containing the system prompt.
	// Relative paths are resolved from the config file's directory.
	PromptFile string `toml:"prompt_file"`
	// ChatType controls conversation management strategy.
	ChatType ChatType `toml:"chat_type"`
	// AllowedModels lists accepted model identifiers. First entry is default.
	AllowedModels []string `toml:"allowed_models"`
	// ReasoningLevel controls how much reasoning the model applies.
	ReasoningLevel ReasoningLevel `toml:"reasoning_level"`
	// MemoryTags are labels used by the memory bank (future feature).
	MemoryTags []string `toml:"memory_tags"`
	// Subagents lists config names that this agent may delegate to.
	Subagents []string `toml:"subagents"`
	// ParentAgent is the config name of this agent's parent, if any.
	ParentAgent string `toml:"parent_agent"`
	// AllowedTools lists tool names the agent may invoke. Empty = all tools.
	AllowedTools []string `toml:"allowed_tools"`
	// AllowedMCPs lists MCP server names the agent may use. Empty = all enabled MCPs.
	AllowedMCPs []string `toml:"allowed_mcps"`
}

// EffectiveShortName returns ShortName, or a compact abbreviation derived
// from Name (uppercase alphanumerics, max 7 chars) when ShortName is unset.
func (c *AgentConfig) EffectiveShortName() string {
	if c.ShortName != "" {
		return c.ShortName
	}
	return DeriveShortName(c.Name)
}

// DeriveShortName abbreviates an agent name to an uppercase key segment of at
// most 7 characters: initials for multi-word names ("QA Lead" → "QAL"),
// a truncated uppercase for single words ("Programmer" → "PROGRAM").
func DeriveShortName(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_'
	})
	var out string
	if len(words) > 1 {
		for _, w := range words {
			for _, r := range w {
				out += string(r)
				break
			}
		}
	} else if len(words) == 1 {
		out = words[0]
	}
	var clean strings.Builder
	for _, r := range strings.ToUpper(out) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		}
	}
	s := clean.String()
	if len(s) > 7 {
		s = s[:7]
	}
	if s == "" {
		s = "AGENT"
	}
	return s
}

// DefaultModel returns the first entry in AllowedModels, or empty string.
func (c *AgentConfig) DefaultModel() string {
	if len(c.AllowedModels) == 0 {
		return ""
	}
	return c.AllowedModels[0]
}

// IsToolAllowed reports whether the named tool may be used by this agent.
// An empty AllowedTools list (or one containing "*") allows all tools.
// Entries ending in "*" match by prefix (e.g. "codegraph_*").
func (c *AgentConfig) IsToolAllowed(name string) bool {
	if len(c.AllowedTools) == 0 {
		return true
	}
	for _, t := range c.AllowedTools {
		if t == "*" || t == name {
			return true
		}
		if n := len(t); n > 1 && t[n-1] == '*' && strings.HasPrefix(name, t[:n-1]) {
			return true
		}
	}
	return false
}
