package engine

import (
	"context"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
)

func (e *NativeEngine) buildSessionPrompt(
	ctx context.Context,
	agent db.Agent,
	task db.Task,
	rootTask db.Task,
	mode string,
	options sessionOptions,
	workspacePath string,
	readOnlyDirs []string,
	artifactDir string,
	rootTaskID int32,
	runID int32,
) (string, []aicli.Message) {
	systemPrompt := strings.TrimSpace(agent.SystemPrompt)
	if options.IncludeTaskContext {
		taskContext := NewSystemPromptBuilder(e.q).Build(agent, task)
		if systemPrompt != "" {
			systemPrompt += "\n\n"
		}
		systemPrompt += taskContext
	}
	systemPrompt += fmt.Sprintf("\nWorkdir: %s", workspacePath)
	systemPrompt += fmt.Sprintf("\nRuntime session ID: %d", runID)
	if options.ReplayHistory != nil {
		systemPrompt += "\nFork replay: completed stateful tool calls have been restored before this session continues."
	}
	if agent.CanUseWorkers {
		systemPrompt += "\n\n" + strings.TrimSpace(agentconfig.MustPrompt("utils/worker_capability.md"))
	}
	if branch := strings.TrimSpace(rootTask.GitHubBranch); branch != "" {
		systemPrompt += fmt.Sprintf("\nTask Git branch: %s (shared by every run and sub-run in this task)", branch)
	}
	if rootTask.GitHubPRNumber != 0 || strings.TrimSpace(rootTask.GitHubPRURL) != "" {
		systemPrompt += fmt.Sprintf("\nTask GitHub PR: #%d %s", rootTask.GitHubPRNumber, strings.TrimSpace(rootTask.GitHubPRURL))
	}
	if len(readOnlyDirs) > 0 {
		systemPrompt += fmt.Sprintf("\nReadable (read-only) dirs: %s", strings.Join(readOnlyDirs, ", "))
	}
	if artifacts, err := e.q.ListArtifactsByTaskTree(ctx, rootTaskID); err == nil && len(artifacts) > 0 {
		systemPrompt += fmt.Sprintf("\n\nArtifacts produced so far (%d, files in %s):\n%s", len(artifacts), artifactDir, formatArtifactList(artifacts))
	}

	initialMessages := options.SeedHistory
	if initialMessages == nil {
		if options.IncludeTaskContext {
			initialMessages = e.buildInitialMessages(ctx, task, mode)
		} else {
			initialMessages = []aicli.Message{{Role: "user", Content: options.Instruction}}
		}
	}
	if options.Instruction != "" && options.IncludeTaskContext && options.SeedHistory == nil {
		initialMessages = append(initialMessages, aicli.Message{Role: "user", Content: strings.TrimSpace(agentconfig.MustPrompt("utils/orchestrator_instruction.md")) + "\n" + options.Instruction})
	}
	return systemPrompt, initialMessages
}
