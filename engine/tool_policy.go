package engine

import (
	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
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
	return []aicli.ToolName{aicli.ToolFinishTask, aicli.ToolReportStatus}
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
	switch agentconfig.CanonicalRole(roleKey) {
	case "CEO", "CTO", "CMO":
		return true
	default:
		return false
	}
}

func RoleDefaultCanUseWorkers(roleKey string) bool { return workerDefaultForRole(roleKey) }

// agentCanUseWorkers preserves the persisted opt-in while recognizing
// built-in role aliases created by older UI flows (for example "CEO Agent").
func agentCanUseWorkers(agent db.Agent) bool {
	if agent.CanUseWorkers || workerDefaultForRole(agent.RoleKey) {
		return true
	}
	return agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CEO") ||
		agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CTO") ||
		agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CMO")
}

// roleToolNames returns the built-in runtime contract for persisted agents.
// Database agents do not carry the TOML AgentConfig's AllowedTools field, so
// role-aware rows must still receive the same least-privilege boundary as
// built-in configurations. An empty result intentionally preserves the
// historical full registry for custom roles.
func roleToolNames(agent db.Agent) []string {
	base := func(groups ...[]aicli.ToolName) []string {
		seen := make(map[string]struct{})
		var names []string
		for _, group := range groups {
			for _, name := range group {
				if _, ok := seen[string(name)]; ok {
					continue
				}
				seen[string(name)] = struct{}{}
				names = append(names, string(name))
			}
		}
		return names
	}

	common := append(messagingToolNames(), artifactToolNames()...)
	common = append(common, agentLifecycleToolNames()...)
	role := agentconfig.CanonicalRole(agent.RoleKey)
	if role == agent.RoleKey || role == "" {
		role = agentconfig.CanonicalRole(agent.Name)
	}
	switch role {
	case "CEO":
		return base(taskToolNames(), common, []aicli.ToolName{aicli.ToolAskHuman})
	case "CTO":
		return base(readOnlyToolNames(), common, workerControlToolNames(), []aicli.ToolName{aicli.ToolCodegraphWildcard})
	case "CMO":
		return base(contentToolNames(), common, workerControlToolNames())
	case "Coder", "Debugger":
		return base(implementationToolNames(), common)
	case "QA":
		return base(qaToolNames(), common)
	case "Designer", "SMM", "PPC Specialist", "Post Writer":
		return base(contentToolNames(), common)
	default:
		return nil
	}
}

func qaToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolRead, aicli.ToolListDir, aicli.ToolGrep, aicli.ToolBash, aicli.ToolWebFetch, aicli.ToolBrowserUse}
}

func readOnlyToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolRead, aicli.ToolListDir, aicli.ToolGrep}
}

func implementationToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolRead, aicli.ToolWrite, aicli.ToolBash, aicli.ToolListDir, aicli.ToolGrep, aicli.ToolCodegraphWildcard}
}

func contentToolNames() []aicli.ToolName {
	return []aicli.ToolName{aicli.ToolRead, aicli.ToolWebFetch}
}
