package tools

import (
	"context"

	"agent-orchestrator/engine/aicli"
)

// WorkerCallbacks is the narrow control-plane surface exposed to a helper
// worker. MCP tools can be added by the engine after this independent registry
// is built; no parent registry is copied.
type WorkerCallbacks struct {
	ReportStatus func(context.Context, string, int64) error
	FinishWork   func(context.Context, FinishWorkResult) (string, error)
}

// NewWorkerRegistry builds a helper registry from scratch. The filesystem
// registry is rooted at workspacePath and only receives read-only input roots.
func NewWorkerRegistry(workspacePath string, readOnlyDirs []string, cb WorkerCallbacks) *aicli.Registry {
	return NewWorkerRegistryWithEnv(workspacePath, readOnlyDirs, nil, cb)
}

// NewWorkerRegistryWithEnv is NewWorkerRegistry with per-run environment
// values injected into its sandboxed shell.
func NewWorkerRegistryWithEnv(workspacePath string, readOnlyDirs []string, extraEnv map[string]string, cb WorkerCallbacks) *aicli.Registry {
	r := DefaultRegistryWithEnv(workspacePath, extraEnv, readOnlyDirs...)
	if cb.ReportStatus != nil {
		r.Register(NewReportStatus(cb.ReportStatus))
	}
	if cb.FinishWork != nil {
		r.Register(NewFinishWork(cb.FinishWork))
	}
	return r
}
