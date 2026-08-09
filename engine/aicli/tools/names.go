package tools

import "agent-orchestrator/engine/aicli/toolnames"

// Re-export canonical names for the per-tool implementations in this
// package. The definitions live in the dependency-free toolnames package so
// the aicli loop can use the same enum without an import cycle.
type Name = toolnames.Name

const (
	ToolBash                  = toolnames.ToolBash
	ToolRead                  = toolnames.ToolRead
	ToolWrite                 = toolnames.ToolWrite
	ToolListDir               = toolnames.ToolListDir
	ToolGrep                  = toolnames.ToolGrep
	ToolWebFetch              = toolnames.ToolWebFetch
	ToolBrowserUse            = toolnames.ToolBrowserUse
	ToolFinishTask            = toolnames.ToolFinishTask
	ToolWriteArtifact         = toolnames.ToolWriteArtifact
	ToolListArtifacts         = toolnames.ToolListArtifacts
	ToolReadArtifact          = toolnames.ToolReadArtifact
	ToolAskArtifact           = toolnames.ToolAskArtifact
	ToolCreateSubtask         = toolnames.ToolCreateSubtask
	ToolAnswerSubtaskQuestion = toolnames.ToolAnswerSubtaskQuestion
	ToolAskTaskOwner          = toolnames.ToolAskTaskOwner
	ToolCreateTask            = toolnames.ToolCreateTask
	ToolAskHuman              = toolnames.ToolAskHuman
	ToolReportStatus          = toolnames.ToolReportStatus
	ToolCallMCP               = toolnames.ToolCallMCP
	ToolDiscoverMCP           = toolnames.ToolDiscoverMCP
	ToolCodegraphWildcard     = toolnames.ToolCodegraphWildcard
)
