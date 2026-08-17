package aicli

// ToolName is the canonical runtime name of an agent tool. Keeping these
// names with the agent loop lets the tool implementations and orchestration
// code share one source of truth without an import cycle.
type ToolName string

const (
	ToolBash              ToolName = "bash"
	ToolRead              ToolName = "read"
	ToolWrite             ToolName = "write"
	ToolListDir           ToolName = "ls"
	ToolGrep              ToolName = "grep"
	ToolWebFetch          ToolName = "web_fetch"
	ToolBrowserUse        ToolName = "browser_use"
	ToolFinishTask        ToolName = "finish_task"
	ToolWriteArtifact     ToolName = "write_artifact"
	ToolListArtifacts     ToolName = "list_artifacts"
	ToolReadArtifact      ToolName = "read_artifact"
	ToolCreateSubtask     ToolName = "create_subtask"
	ToolAskTaskOwner      ToolName = "ask_task_owner"
	ToolCreateTask        ToolName = "create_task"
	ToolGetTask           ToolName = "get_task"
	ToolAskHuman          ToolName = "ask_human"
	ToolReportStatus      ToolName = "report_status"
	ToolAnswerMessage     ToolName = "answer_message"
	ToolFinishWork        ToolName = "finish_work"
	ToolRunWorker         ToolName = "run_worker"
	ToolWorkerList        ToolName = "worker_list"
	ToolGetWorkerInfo     ToolName = "get_worker_info"
	ToolStopWorker        ToolName = "stop_worker"
	ToolAskCEO            ToolName = "ask_ceo"
	ToolCallMCP           ToolName = "call_mcp_tool"
	ToolDiscoverMCP       ToolName = "discover_mcp_tool"
	ToolCodegraphWildcard ToolName = "codegraph_*"
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
		// All other capabilities are derived from role, persisted settings,
		// connected MCPs, and transient runtime state.
	)
}
