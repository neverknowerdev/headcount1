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

//go:embed prompts/codeexplorer.md
var codeExplorerPrompt string

// Shared tool bundles. AllowedTools supports exact names and "prefix*"
// wildcards (see aicli.Registry.Filter). Every agent needs finish_task; the
// artifact read tools are universal so agents can consume each other's
// deliverables instead of re-deriving them.
var (
	coordinationTools = []string{
		"finish_task", "create_subtask", "expand_run_result",
		"list_artifacts", "read_artifact", "update_task_details",
	}
	readOnlyCodeTools = []string{
		"read", "ls", "grep", "codegraph_*",
	}
	artifactTools = []string{
		"list_artifacts", "read_artifact", "write_artifact",
	}
)

func join(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
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
			ShortName:      "CEO",
			Description:    "Chief Executive Officer — orchestrates task execution through delegation",
			Prompt:         strings.TrimSpace(ceoPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"CTO", "Writer", "Researcher", "CodeExplorer"},
			// The CEO coordinates; it gets no file, shell, web, or authoring
			// tools so deliverables always come from specialists.
			AllowedTools: coordinationTools,
		},
		{
			Name:           "CTO",
			ShortName:      "CTO",
			Description:    "Chief Technology Officer — technical architecture and engineering leadership",
			Prompt:         strings.TrimSpace(ctoPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"Programmer", "QA", "Researcher", "CodeExplorer"},
			ParentAgent:    "CEO",
			AllowedTools:   join(coordinationTools, readOnlyCodeTools),
		},
		{
			Name:           "Programmer",
			ShortName:      "PROG",
			Description:    "Software developer — implements features and fixes bugs",
			Prompt:         strings.TrimSpace(programmerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
			AllowedTools: join(readOnlyCodeTools, []string{
				"finish_task", "expand_run_result", "write", "bash", "web_fetch",
				"list_artifacts", "read_artifact",
			}),
		},
		{
			Name:           "QA",
			ShortName:      "QA",
			Description:    "Quality assurance — independently verifies deliverables, tests, and reports defects",
			Prompt:         strings.TrimSpace(qaPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
			AllowedTools: join(readOnlyCodeTools, artifactTools, []string{
				"finish_task", "expand_run_result", "verify_spec_items",
				"bash", "browser_use",
			}),
		},
		{
			Name:           "Writer",
			ShortName:      "WRITER",
			Description:    "Technical writer — creates documentation, reports, and summaries",
			Prompt:         strings.TrimSpace(writerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
			AllowedTools: join(readOnlyCodeTools, artifactTools, []string{
				"finish_task", "expand_run_result", "web_fetch",
			}),
		},
		{
			Name:           "Researcher",
			ShortName:      "RSRCH",
			Description:    "Researcher — investigates external topics and synthesises findings",
			Prompt:         strings.TrimSpace(researcherPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools: join(readOnlyCodeTools, artifactTools, []string{
				"finish_task", "expand_run_result", "web_fetch", "browser_use",
				"call_mcp_tool", "discover_mcp_tool",
			}),
		},
		{
			Name:           "CodeExplorer",
			ShortName:      "EXPLORE",
			Description:    "Codebase explorer — maps architecture, features, and implementation state",
			Prompt:         strings.TrimSpace(codeExplorerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools: join(readOnlyCodeTools, artifactTools, []string{
				"finish_task", "expand_run_result", "bash",
			}),
		},
	}
}
