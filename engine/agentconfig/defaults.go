package agentconfig

import (
	_ "embed"
	"strings"

	"agent-orchestrator/engine/aicli"
)

//go:embed prompts/ceo.md
var ceoPrompt string

//go:embed prompts/cto.md
var ctoPrompt string

//go:embed prompts/cmo.md
var cmoPrompt string

//go:embed prompts/coder.md
var coderPrompt string

//go:embed prompts/debugger.md
var debuggerPrompt string

//go:embed prompts/qa.md
var qaPrompt string

//go:embed prompts/designer.md
var designerPrompt string

//go:embed prompts/smm.md
var smmPrompt string

//go:embed prompts/ppc.md
var ppcPrompt string

//go:embed prompts/post_writer.md
var postWriterPrompt string

// Tool sets per role. The engine derives lifecycle, task, messaging, worker,
// and MCP capabilities from the actor and current runtime state; these lists
// remain compatibility metadata for the built-in configuration endpoint.

// ceoTools: the CEO delegates all real work — no file/shell/web access.
// It can inspect artifacts and own durable task planning; execution is
// delegated through the task orchestrator.
// read_file is withheld too — the artifacts dir is readable by the file
// sandbox, so read_file would be a trivial bypass of that restriction.
var ceoTools = aicli.Names(
	aicli.ToolCreateSubtask,
	aicli.ToolCreateTask,
	aicli.ToolGetTask,
	aicli.ToolAskHuman,
	aicli.ToolReportStatus,
	aicli.ToolFinishTask,
	aicli.ToolListArtifacts,
)

// ctoTools: the CTO explores code (codegraph + read-only file tools), writes
// specs as artifacts, and delegates implementation. These names must match
// the runtime registry names in engine/aicli/tools/default.go.
var ctoTools = aicli.Names(
	aicli.ToolCodegraphWildcard,
	aicli.ToolAskTaskOwner,
	aicli.ToolAskHuman,
	aicli.ToolReportStatus,
	aicli.ToolFinishTask,
	aicli.ToolRead,
	aicli.ToolListDir,
	aicli.ToolGrep,
	aicli.ToolListArtifacts,
	aicli.ToolReadArtifact,
	aicli.ToolWriteArtifact,
)

// cmoTools: the CMO plans and delegates marketing work, owning strategy docs.
var cmoTools = aicli.Names(
	aicli.ToolAskTaskOwner,
	aicli.ToolAskHuman,
	aicli.ToolReportStatus,
	aicli.ToolFinishTask,
	aicli.ToolRead,
	aicli.ToolWebFetch,
	aicli.ToolListArtifacts,
	aicli.ToolReadArtifact,
	aicli.ToolWriteArtifact,
)

// implementerTools: full workspace access for agents that write code. Keep
// these names aligned with the actual Tool.Def names: the previous
// read_file/write_file/exec_command/list_dir names filtered out the tools and
// left Coder with no shell or edit capability.
var implementerTools = aicli.Names(
	aicli.ToolRead,
	aicli.ToolWrite,
	aicli.ToolBash,
	aicli.ToolListDir,
	aicli.ToolGrep,
	aicli.ToolCodegraphWildcard,
	aicli.ToolAskTaskOwner,
	aicli.ToolReportStatus,
	aicli.ToolFinishTask,
	aicli.ToolListArtifacts,
	aicli.ToolReadArtifact,
	aicli.ToolWriteArtifact,
)

// qaTools: QA verifies — reads, runs, and drives a browser, but never edits.
var qaTools = aicli.Names(
	aicli.ToolRead,
	aicli.ToolListDir,
	aicli.ToolGrep,
	aicli.ToolBash,
	aicli.ToolWebFetch,
	aicli.ToolBrowserUse,
	aicli.ToolAskTaskOwner,
	aicli.ToolReportStatus,
	aicli.ToolFinishTask,
	aicli.ToolListArtifacts,
	aicli.ToolReadArtifact,
	aicli.ToolWriteArtifact,
)

// contentTools: research + artifact writing for content/design specialists.
var contentTools = aicli.Names(
	aicli.ToolRead,
	aicli.ToolWebFetch,
	aicli.ToolAskTaskOwner,
	aicli.ToolReportStatus,
	aicli.ToolFinishTask,
	aicli.ToolListArtifacts,
	aicli.ToolReadArtifact,
	aicli.ToolWriteArtifact,
)

// BuiltinConfigs returns the predefined agent configurations in their
// canonical display order (unlike Factory.ListNames, which is unordered).
func BuiltinConfigs() []*AgentConfig {
	return builtinConfigs()
}

// builtinConfigs returns the set of predefined agent configurations.
//
// Hierarchy:
//
//	CEO
//	  CTO  → Coder, Debugger, QA
//	  CMO  → SMM, PPC Specialist, Post Writer
//	  Designer
//
// AllowedModels is intentionally left empty in all builtin configs so that
// the engine's model resolver uses the configured LLM provider's available
// models rather than hardcoded provider-specific names. Add models to
// AllowedModels in a custom TOML config when you need to pin a specific model.
func builtinConfigs() []*AgentConfig {
	return []*AgentConfig{
		{
			Name:           "CEO",
			ShortName:      "CEO",
			Description:    "Chief Executive Officer — owns overall project execution and business decisions, works exclusively through delegation",
			Prompt:         strings.TrimSpace(ceoPrompt),
			ChatType:       ChatTypeCompactThinking,
			ReasoningLevel: ReasoningLevelMax,
			CanUseWorkers:  true,
			AllowedTools:   ceoTools,
		},
		{
			Name:           "CTO",
			ShortName:      "CTO",
			Description:    "Chief Technology Officer — owns architecture and tech specs, delegates implementation to Coder, Debugger and QA",
			Prompt:         strings.TrimSpace(ctoPrompt),
			ChatType:       ChatTypeCompactThinking,
			ReasoningLevel: ReasoningLevelMax,
			CanUseWorkers:  true,
			AllowedTools:   ctoTools,
		},
		{
			Name:           "CMO",
			ShortName:      "CMO",
			Description:    "Chief Marketing Officer — owns marketing strategy and metrics, delegates execution to SMM, PPC Specialist and Post Writer",
			Prompt:         strings.TrimSpace(cmoPrompt),
			ChatType:       ChatTypeCompactThinking,
			ReasoningLevel: ReasoningLevelMax,
			CanUseWorkers:  true,
			AllowedTools:   cmoTools,
		},
		{
			Name:           "Coder",
			ShortName:      "CODER",
			Description:    "Coder — implements features from tech specs with high-quality, pattern-following code",
			Prompt:         strings.TrimSpace(coderPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   implementerTools,
		},
		{
			Name:           "Debugger",
			ShortName:      "DEBUG",
			Description:    "Debugger — reproduces, diagnoses and fixes bugs at their root cause",
			Prompt:         strings.TrimSpace(debuggerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   implementerTools,
		},
		{
			Name:           "QA",
			ShortName:      "QA",
			Description:    "Quality assurance — verifies implementations against their spec; tests UI changes in a real browser",
			Prompt:         strings.TrimSpace(qaPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   qaTools,
		},
		{
			Name:           "Designer",
			ShortName:      "DSGN",
			Description:    "Designer — produces UI/UX design specifications",
			Prompt:         strings.TrimSpace(designerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   contentTools,
		},
		{
			Name:           "SMM",
			ShortName:      "SMM",
			Description:    "Social media marketing — posts, announcements, and content plans",
			Prompt:         strings.TrimSpace(smmPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   contentTools,
		},
		{
			Name:           "PPC Specialist",
			ShortName:      "PPC",
			Description:    "PPC Specialist — paid ad campaigns: structure, keywords, budgets, and ad copy",
			Prompt:         strings.TrimSpace(ppcPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   contentTools,
		},
		{
			Name:           "Post Writer",
			ShortName:      "POST",
			Description:    "Post Writer — long-form content: blog posts, articles, and announcements",
			Prompt:         strings.TrimSpace(postWriterPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			AllowedTools:   contentTools,
		},
	}
}
