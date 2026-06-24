package agentconfig

import (
	_ "embed"
	"strings"
)

//go:embed prompts/ceo.md
var ceoPrompt string

//go:embed prompts/cto.md
var ctoPrompt string

//go:embed prompts/programmer.md
var programmerPrompt string

//go:embed prompts/qa.md
var qaPrompt string

//go:embed prompts/writer.md
var writerPrompt string

//go:embed prompts/researcher.md
var researcherPrompt string

// builtinConfigs returns the set of predefined agent configurations.
// AllowedModels is intentionally left empty in all builtin configs so that
// the engine's model resolver uses the configured LLM provider's available
// models rather than hardcoded provider-specific names. Add models to
// AllowedModels in a custom TOML config when you need to pin a specific model.
func builtinConfigs() []*AgentConfig {
	return []*AgentConfig{
		{
			Name:           "CEO",
			Description:    "Chief Executive Officer — strategic oversight and decision making",
			Prompt:         strings.TrimSpace(ceoPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"CTO", "Writer", "Researcher"},
		},
		{
			Name:           "CTO",
			Description:    "Chief Technology Officer — technical architecture and engineering leadership",
			Prompt:         strings.TrimSpace(ctoPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"Programmer", "QA", "Researcher"},
			ParentAgent:    "CEO",
		},
		{
			Name:           "Programmer",
			Description:    "Software developer — implements features and fixes bugs",
			Prompt:         strings.TrimSpace(programmerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
		},
		{
			Name:           "QA",
			Description:    "Quality assurance — tests, validates, and reports defects",
			Prompt:         strings.TrimSpace(qaPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
		},
		{
			Name:           "Writer",
			Description:    "Technical writer — creates documentation, reports, and summaries",
			Prompt:         strings.TrimSpace(writerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
		},
		{
			Name:           "Researcher",
			Description:    "Researcher — investigates topics and synthesises findings",
			Prompt:         strings.TrimSpace(researcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
		},
	}
}
