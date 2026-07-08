package agentconfig

import (
	_ "embed"
	"strings"
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

// Tool sets per role. An agent only ever sees tools from its own list; the
// engine additionally registers create_subtask / answer_subtask_question only
// while the delegation depth cap allows it, and ask_task_owner only in
// delegated (child) sessions.

// ceoTools: the CEO delegates all real work — no file/shell/web access.
// It can LIST artifacts and VERIFY their content by asking targeted
// questions (ask_artifact — answered by a separate cheap reader call), but
// deliberately cannot READ full artifact content: consuming deliverables is
// the sub-agents' job, and the CEO acts on their finish_task handoffs.
// read_file is withheld too — the artifacts dir is readable by the file
// sandbox, so read_file would be a trivial bypass of that restriction.
var ceoTools = []string{
	"create_subtask",
	"answer_subtask_question",
	"create_task",
	"ask_human",
	"report_status",
	"memory_recall",
	"finish_task",
	"list_artifacts",
	"ask_artifact",
}

// ctoTools: the CTO explores code (codegraph + read-only file tools), writes
// specs as artifacts, and delegates implementation.
var ctoTools = []string{
	"codegraph_*",
	"create_subtask",
	"answer_subtask_question",
	"ask_task_owner",
	"ask_human",
	"report_status",
	"memory_recall",
	"finish_task",
	"read_file",
	"list_dir",
	"grep",
	"list_artifacts",
	"read_artifact",
	"ask_artifact",
	"write_artifact",
}

// cmoTools: the CMO plans and delegates marketing work, owning strategy docs.
var cmoTools = []string{
	"create_subtask",
	"answer_subtask_question",
	"ask_task_owner",
	"ask_human",
	"report_status",
	"memory_recall",
	"finish_task",
	"read_file",
	"web_fetch",
	"list_artifacts",
	"read_artifact",
	"ask_artifact",
	"write_artifact",
}

// implementerTools: full workspace access for agents that write code.
var implementerTools = []string{
	"read_file",
	"write_file",
	"exec_command",
	"list_dir",
	"grep",
	"codegraph_*",
	"ask_task_owner",
	"report_status",
	"memory_recall",
	"finish_task",
	"list_artifacts",
	"read_artifact",
	"write_artifact",
}

// qaTools: QA verifies — reads, runs, and drives a browser, but never edits.
var qaTools = []string{
	"read_file",
	"list_dir",
	"grep",
	"exec_command",
	"web_fetch",
	"browser_use",
	"ask_task_owner",
	"report_status",
	"memory_recall",
	"finish_task",
	"list_artifacts",
	"read_artifact",
	"write_artifact",
}

// contentTools: research + artifact writing for content/design specialists.
var contentTools = []string{
	"read_file",
	"web_fetch",
	"ask_task_owner",
	"report_status",
	"memory_recall",
	"finish_task",
	"list_artifacts",
	"read_artifact",
	"write_artifact",
}

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
			Subagents:      []string{"CTO", "CMO", "Designer"},
			AllowedTools:   ceoTools,
		},
		{
			Name:           "CTO",
			ShortName:      "CTO",
			Description:    "Chief Technology Officer — owns architecture and tech specs, delegates implementation to Coder, Debugger and QA",
			Prompt:         strings.TrimSpace(ctoPrompt),
			ChatType:       ChatTypeCompactThinking,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"Coder", "Debugger", "QA"},
			ParentAgent:    "CEO",
			AllowedTools:   ctoTools,
		},
		{
			Name:           "CMO",
			ShortName:      "CMO",
			Description:    "Chief Marketing Officer — owns marketing strategy and metrics, delegates execution to SMM, PPC Specialist and Post Writer",
			Prompt:         strings.TrimSpace(cmoPrompt),
			ChatType:       ChatTypeCompactThinking,
			ReasoningLevel: ReasoningLevelMax,
			Subagents:      []string{"SMM", "PPC Specialist", "Post Writer"},
			ParentAgent:    "CEO",
			AllowedTools:   cmoTools,
		},
		{
			Name:           "Coder",
			ShortName:      "CODER",
			Description:    "Coder — implements features from tech specs with high-quality, pattern-following code",
			Prompt:         strings.TrimSpace(coderPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
			AllowedTools:   implementerTools,
		},
		{
			Name:           "Debugger",
			ShortName:      "DEBUG",
			Description:    "Debugger — reproduces, diagnoses and fixes bugs at their root cause",
			Prompt:         strings.TrimSpace(debuggerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
			AllowedTools:   implementerTools,
		},
		{
			Name:           "QA",
			ShortName:      "QA",
			Description:    "Quality assurance — verifies implementations against their spec; tests UI changes in a real browser",
			Prompt:         strings.TrimSpace(qaPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CTO",
			AllowedTools:   qaTools,
		},
		{
			Name:           "Designer",
			ShortName:      "DSGN",
			Description:    "Designer — produces UI/UX design specifications",
			Prompt:         strings.TrimSpace(designerPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CEO",
			AllowedTools:   contentTools,
		},
		{
			Name:           "SMM",
			ShortName:      "SMM",
			Description:    "Social media marketing — posts, announcements, and content plans",
			Prompt:         strings.TrimSpace(smmPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CMO",
			AllowedTools:   contentTools,
		},
		{
			Name:           "PPC Specialist",
			ShortName:      "PPC",
			Description:    "PPC Specialist — paid ad campaigns: structure, keywords, budgets, and ad copy",
			Prompt:         strings.TrimSpace(ppcPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CMO",
			AllowedTools:   contentTools,
		},
		{
			Name:           "Post Writer",
			ShortName:      "POST",
			Description:    "Post Writer — long-form content: blog posts, articles, and announcements",
			Prompt:         strings.TrimSpace(postWriterPrompt),
			ChatType:       ChatTypeMessageHistory,
			ReasoningLevel: ReasoningLevelMedium,
			ParentAgent:    "CMO",
			AllowedTools:   contentTools,
		},
	}
}
