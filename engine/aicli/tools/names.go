package tools

import "agent-orchestrator/engine/aicli"

// Re-export canonical names for the per-tool implementations in this
// package. The definitions live in aicli so the agent loop and tool
// implementations share the same enum without an import cycle.
type Name = aicli.ToolName

const (
	ToolBash                  = aicli.ToolBash
	ToolRead                  = aicli.ToolRead
	ToolWrite                 = aicli.ToolWrite
	ToolListDir               = aicli.ToolListDir
	ToolGrep                  = aicli.ToolGrep
	ToolWebFetch              = aicli.ToolWebFetch
	ToolBrowserUse            = aicli.ToolBrowserUse
	ToolFinishTask            = aicli.ToolFinishTask
	ToolWriteArtifact         = aicli.ToolWriteArtifact
	ToolListArtifacts         = aicli.ToolListArtifacts
	ToolReadArtifact          = aicli.ToolReadArtifact
	ToolAskArtifact           = aicli.ToolAskArtifact
	ToolCreateSubtask         = aicli.ToolCreateSubtask
	ToolAnswerSubtaskQuestion = aicli.ToolAnswerSubtaskQuestion
	ToolAskTaskOwner          = aicli.ToolAskTaskOwner
	ToolCreateTask            = aicli.ToolCreateTask
	ToolAskHuman              = aicli.ToolAskHuman
	ToolReportStatus          = aicli.ToolReportStatus
	ToolCallMCP               = aicli.ToolCallMCP
	ToolDiscoverMCP           = aicli.ToolDiscoverMCP
	ToolCodegraphWildcard     = aicli.ToolCodegraphWildcard
)
