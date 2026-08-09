package toolnames

// Name is the canonical runtime name of an agent tool. Keep tool names in
// this package so registry definitions, role allowlists, filters, and
// lifecycle handling cannot silently drift apart.
type Name string

const (
	ToolBash                  Name = "bash"
	ToolRead                  Name = "read"
	ToolWrite                 Name = "write"
	ToolListDir               Name = "ls"
	ToolGrep                  Name = "grep"
	ToolWebFetch              Name = "web_fetch"
	ToolBrowserUse            Name = "browser_use"
	ToolFinishTask            Name = "finish_task"
	ToolWriteArtifact         Name = "write_artifact"
	ToolListArtifacts         Name = "list_artifacts"
	ToolReadArtifact          Name = "read_artifact"
	ToolAskArtifact           Name = "ask_artifact"
	ToolCreateSubtask         Name = "create_subtask"
	ToolAnswerSubtaskQuestion Name = "answer_subtask_question"
	ToolAskTaskOwner          Name = "ask_task_owner"
	ToolCreateTask            Name = "create_task"
	ToolAskHuman              Name = "ask_human"
	ToolReportStatus          Name = "report_status"
	ToolCallMCP               Name = "call_mcp_tool"
	ToolDiscoverMCP           Name = "discover_mcp_tool"
	ToolCodegraphWildcard     Name = "codegraph_*"
)

// Names converts canonical tool names to the string slices expected by the
// configuration and registry APIs.
func Names(names ...Name) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = string(name)
	}
	return result
}
