package aicli

// ToolName is the canonical runtime name of an agent tool. Keeping these
// names with the agent loop lets the tool implementations and orchestration
// code share one source of truth without an import cycle.
type ToolName string

const (
	ToolBash                  ToolName = "bash"
	ToolRead                  ToolName = "read"
	ToolWrite                 ToolName = "write"
	ToolListDir               ToolName = "ls"
	ToolGrep                  ToolName = "grep"
	ToolWebFetch              ToolName = "web_fetch"
	ToolBrowserUse            ToolName = "browser_use"
	ToolFinishTask            ToolName = "finish_task"
	ToolWriteArtifact         ToolName = "write_artifact"
	ToolListArtifacts         ToolName = "list_artifacts"
	ToolReadArtifact          ToolName = "read_artifact"
	ToolAskArtifact           ToolName = "ask_artifact"
	ToolCreateSubtask         ToolName = "create_subtask"
	ToolAnswerSubtaskQuestion ToolName = "answer_subtask_question"
	ToolAskTaskOwner          ToolName = "ask_task_owner"
	ToolCreateTask            ToolName = "create_task"
	ToolAskHuman              ToolName = "ask_human"
	ToolReportStatus          ToolName = "report_status"
	ToolGetSessions           ToolName = "get_sessions"
	ToolGetSessionStatus      ToolName = "get_session_status"
	ToolRunNewSession         ToolName = "run_new_session"
	ToolStopSession           ToolName = "stop_session"
	ToolForkSession           ToolName = "fork_session"
	ToolCallMCP               ToolName = "call_mcp_tool"
	ToolDiscoverMCP           ToolName = "discover_mcp_tool"
	ToolCodegraphWildcard     ToolName = "codegraph_*"
)

// Names converts canonical tool names to the string slices expected by the
// configuration and registry APIs.
func Names(names ...ToolName) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = string(name)
	}
	return result
}

// OrchestratorToolNames is the complete allowlist for a sidecar task
// orchestrator. Keep this separate from ConfigurableToolNames: these tools
// are runtime-managed and must never be granted to a worker accidentally.
func OrchestratorToolNames() []string {
	return Names(ToolGetSessions, ToolGetSessionStatus, ToolAskTaskOwner,
		ToolRunNewSession, ToolStopSession, ToolForkSession)
}

// ConfigurableToolNames returns the native tools that can be enabled or
// disabled from the custom-agent permissions UI. Lifecycle tools stay out of
// this list so an agent cannot be configured without a way to finish or report
// its task.
func ConfigurableToolNames() []string {
	return Names(
		ToolBash,
		ToolRead,
		ToolWrite,
		ToolListDir,
		ToolGrep,
		ToolWebFetch,
		ToolBrowserUse,
		ToolWriteArtifact,
		ToolListArtifacts,
		ToolReadArtifact,
		ToolAskArtifact,
		ToolCreateSubtask,
		ToolAnswerSubtaskQuestion,
		ToolAskTaskOwner,
		ToolCreateTask,
	)
}
