package tools

import (
	"agent-orchestrator/engine/aicli"
)

// DefaultRegistry returns a Registry pre-loaded with the built-in file, shell,
// and web tools, all sandboxed to workspacePath.
// Codegraph tools are added separately via CodegraphProxy in the engine.
func DefaultRegistry(workspacePath string) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(NewReadFile(workspacePath))
	r.Register(NewWriteFile(workspacePath))
	r.Register(NewListDir(workspacePath))
	r.Register(NewExecCommand(workspacePath))
	r.Register(NewGrep(workspacePath))
	r.Register(NewWebFetch())
	return r
}
