package engine

import (
	"context"
	"testing"

	"agent-orchestrator/engine/aicli/tools"
	"github.com/stretchr/testify/require"
)

func TestApplyWorkerToolPolicyInheritKeepsParentCeiling(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())
	filtered := applyWorkerToolPolicy(registry, `{"bash":"deny"}`, `{"mode":"inherit"}`)
	require.NotContains(t, filtered.Names(), "bash")
	require.Contains(t, filtered.Names(), "read")
}

func TestApplyWorkerToolPolicyDenyOnlyRemovesSelectedTools(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())
	filtered := applyWorkerToolPolicy(registry, `{"bash":"deny"}`, `{"mode":"deny","denied":["read"]}`)
	require.NotContains(t, filtered.Names(), "bash")
	require.NotContains(t, filtered.Names(), "read")
	require.Contains(t, filtered.Names(), "grep")
}

func TestApplyWorkerToolPolicyCustomUsesAllowlist(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())
	registry.Register(tools.NewFinishTask(true, func(context.Context, tools.FinishTaskResult) error { return nil }))
	registry.Register(tools.NewReportStatus(func(context.Context, string, int64) error { return nil }))
	filtered := applyWorkerToolPolicy(registry, `{"bash":"deny"}`, `{"mode":"custom","allowed":["read","grep"]}`)
	require.Equal(t, []string{"finish_task", "grep", "read", "report_status"}, filtered.Names())
}

func TestWorkerMCPAllowedInheritsAndAppliesDenyOrCustom(t *testing.T) {
	require.True(t, workerMCPAllowed("github", `[]`, `{"mode":"inherit"}`))
	require.False(t, workerMCPAllowed("github", `["github"]`, `{"mode":"deny","denied":["github"]}`))
	require.False(t, workerMCPAllowed("slack", `["github"]`, `{"mode":"inherit"}`))
	require.True(t, workerMCPAllowed("github", `[]`, `{"mode":"custom","allowed":["github"]}`))
	require.False(t, workerMCPAllowed("slack", `[]`, `{"mode":"custom","allowed":["github"]}`))
}
