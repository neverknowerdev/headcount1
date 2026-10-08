package aicli

// ToolName is the canonical runtime name of an agent tool. Keeping these
// names with the agent loop lets the tool implementations and the engine
// share one source of truth without an import cycle.
type ToolName string

const (
	ToolBash              ToolName = "bash"
	ToolRead              ToolName = "read"
	ToolWrite             ToolName = "write"
	ToolListDir           ToolName = "ls"
	ToolGrep              ToolName = "grep"
	ToolWebFetch          ToolName = "web_fetch"
	ToolBrowserUse        ToolName = "browser_use"
	ToolWriteArtifact     ToolName = "write_artifact"
	ToolListArtifacts     ToolName = "list_artifacts"
	ToolReadArtifact      ToolName = "read_artifact"
	ToolFinishWork        ToolName = "finish_work"
	ToolCheckpoint        ToolName = "checkpoint"
	ToolCallMCP           ToolName = "call_mcp_tool"
	ToolDiscoverMCP       ToolName = "discover_mcp_tool"
	ToolCodegraphWildcard ToolName = "codegraph_*"
)
