package engine

import (
	"strings"

	"agent-orchestrator/engine/aicli"
)

// Tool groups are deliberately returned as fresh slices. Callers may filter
// or append without mutating another actor's capability policy.
func basicToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolBash, aicli.ToolRead, aicli.ToolWrite, aicli.ToolListDir, aicli.ToolGrep, aicli.ToolWebFetch, aicli.ToolBrowserUse}
}

func BasicToolNames() []aicli.ToolName { return basicToolNames() }

func artifactToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolWriteArtifact, aicli.ToolListArtifacts, aicli.ToolReadArtifact}
}
func ArtifactToolNames() []aicli.ToolName { return artifactToolNames() }

func taskToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolCreateTask, aicli.ToolCreateSubtask, aicli.ToolGetTask}
}
func TaskToolNames() []aicli.ToolName { return taskToolNames() }

func messagingToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolAskTaskOwner, aicli.ToolAnswerMessage}
}
func MessagingToolNames() []aicli.ToolName { return messagingToolNames() }

func agentLifecycleToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolFinishTask, aicli.ToolReportStatus, aicli.ToolAskHuman}
}
func AgentLifecycleToolNames() []aicli.ToolName { return agentLifecycleToolNames() }

func workerControlToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolRunWorker, aicli.ToolWorkerList, aicli.ToolGetWorkerInfo, aicli.ToolStopWorker}
}
func WorkerControlToolNames() []aicli.ToolName { return workerControlToolNames() }

func workerLifecycleToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolReportStatus, aicli.ToolFinishWork}
}
func WorkerLifecycleToolNames() []aicli.ToolName { return workerLifecycleToolNames() }

func orchestratorToolNames() []aicli.ToolName {
	return []aicli.ToolName{"get_session_list", "get_session", "send_message_to_session", "run_new_session", "stop_session", "fork_session", aicli.ToolAskCEO, aicli.ToolAnswerMessage}
}
func OrchestratorToolNames() []aicli.ToolName { return orchestratorToolNames() }

func workerDefaultForRole(roleKey string) bool {
	switch strings.ToUpper(strings.TrimSpace(roleKey)) {
	case "CEO", "CTO", "CMO":
		return true
	default:
		return false
	}
}

func RoleDefaultCanUseWorkers(roleKey string) bool { return workerDefaultForRole(roleKey) }
