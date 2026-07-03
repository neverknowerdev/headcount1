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

//go:embed prompts/qa_lead.md
var qaLeadPrompt string

//go:embed prompts/writer.md
var writerPrompt string

//go:embed prompts/researcher.md
var researcherPrompt string

//go:embed prompts/tech_spec_researcher.md
var techSpecResearcherPrompt string

//go:embed prompts/design_spec_researcher.md
var designSpecResearcherPrompt string

//go:embed prompts/designer.md
var designerPrompt string

//go:embed prompts/smm.md
var smmPrompt string

// ceoAllowedTools restricts the CEO to orchestration-only tools: the CEO
// delegates all real work and therefore gets no file/shell/web access.
var ceoAllowedTools = []string{
	"delegate_task",
	"ask_human",
	"report_status",
	"update_task_details",
	"verify_implementation",
	"expand_run_result",
	"write_artifact",
	"finish_task",
}

// BuiltinConfigs returns the predefined agent configurations in their
// canonical display order (unlike Factory.ListNames, which is unordered).
func BuiltinConfigs() []*AgentConfig {
	return builtinConfigs()
}

// builtinConfigs returns the set of predefined agent configurations.
// AllowedModels is intentionally left empty in all builtin configs so that
// the engine's model resolver uses the configured LLM provider's available
// models rather than hardcoded provider-specific names. Add models to
// AllowedModels in a custom TOML config when you need to pin a specific model.
func builtinConfigs() []*AgentConfig {
	return []*AgentConfig{
		{
			Name:           "CEO",
			Description:    "Chief Executive Officer — orchestrates task execution through delegation",
			Prompt:         strings.TrimSpace(ceoPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"CTO", "Programmer", "QA Lead", "QA", "TechSpecResearcher", "DesignSpecResearcher", "Designer", "SMM", "Writer", "Researcher"},
			AllowedTools:   ceoAllowedTools,
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
			Name:           "QA Lead",
			Description:    "QA Lead — defines acceptance criteria and test cases",
			Prompt:         strings.TrimSpace(qaLeadPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
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
			Name:           "TechSpecResearcher",
			Description:    "Technical researcher — investigates libraries, APIs, and implementation approaches",
			Prompt:         strings.TrimSpace(techSpecResearcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
		},
		{
			Name:           "DesignSpecResearcher",
			Description:    "Design researcher — investigates UX patterns and design conventions",
			Prompt:         strings.TrimSpace(designSpecResearcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
		},
		{
			Name:           "Designer",
			Description:    "Designer — produces UI/UX design specifications",
			Prompt:         strings.TrimSpace(designerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
		},
		{
			Name:           "SMM",
			Description:    "Social media marketing — posts, announcements, and content plans",
			Prompt:         strings.TrimSpace(smmPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
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
