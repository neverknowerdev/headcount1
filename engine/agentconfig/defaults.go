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

//go:embed prompts/smart_planner.md
var smartPlannerPrompt string

//go:embed prompts/tech_spec_researcher.md
var techSpecResearcherPrompt string

//go:embed prompts/writing_spec_researcher.md
var writingSpecResearcherPrompt string

//go:embed prompts/design_spec_researcher.md
var designSpecResearcherPrompt string

//go:embed prompts/coder.md
var coderPrompt string

//go:embed prompts/tester.md
var testerPrompt string

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
		{
			Name:           "SmartPlanner",
			Description:    "SmartPlanner — orchestrates task execution by gathering information and creating specifications",
			Prompt:         strings.TrimSpace(smartPlannerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMax,
			AllowedTools:   []string{"ask_question", "finish_refinement", "finish_task", "expand_run_result"},
		},
		{
			Name:           "TechSpecResearcher",
			Description:    "TechSpecResearcher — answers research questions about the codebase and technical topics",
			Prompt:         strings.TrimSpace(techSpecResearcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   []string{"read", "write", "list", "grep", "exec", "web_fetch", "call_mcp_tool", "discover_mcp_tool", "answer_question"},
		},
		{
			Name:           "WritingSpecResearcher",
			Description:    "WritingSpecResearcher — answers research questions about writing tasks and documentation",
			Prompt:         strings.TrimSpace(writingSpecResearcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   []string{"read", "write", "list", "grep", "exec", "web_fetch", "call_mcp_tool", "discover_mcp_tool", "answer_question"},
		},
		{
			Name:           "DesignSpecResearcher",
			Description:    "DesignSpecResearcher — answers research questions about design tasks and UI patterns",
			Prompt:         strings.TrimSpace(designSpecResearcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   []string{"read", "write", "list", "grep", "exec", "web_fetch", "call_mcp_tool", "discover_mcp_tool", "answer_question"},
		},
		{
			Name:           "Coder",
			Description:    "Coder — implements tasks based on specifications",
			Prompt:         strings.TrimSpace(coderPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   []string{},
		},
		{
			Name:           "Tester",
			Description:    "Tester — tests implemented changes against acceptance criteria",
			Prompt:         strings.TrimSpace(testerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   []string{"browser_use", "read", "list", "grep", "exec", "call_mcp_tool", "discover_mcp_tool", "ask_task_owner", "answer_question"},
		},
	}
}
