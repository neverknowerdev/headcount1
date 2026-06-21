package tools

import (
	"agent-orchestrator/engine/aicli"
)

// DefaultRegistry returns a Registry pre-loaded with the built-in file, shell,
// web, and codegraph tools, all sandboxed to workspacePath.
func DefaultRegistry(workspacePath string) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(NewReadFile(workspacePath))
	r.Register(NewWriteFile(workspacePath))
	r.Register(NewListDir(workspacePath))
	r.Register(NewExecCommand(workspacePath))
	r.Register(NewGrep(workspacePath))
	r.Register(NewWebFetch())
	r.Register(NewCodegraphInit(workspacePath))
	r.Register(NewCodegraphQuery(workspacePath))
	r.Register(NewCodegraphExplore(workspacePath))
	r.Register(NewCodegraphCallers(workspacePath))
	r.Register(NewCodegraphCallees(workspacePath))
	r.Register(NewCodegraphImpact(workspacePath))
	r.Register(NewCodegraphNode(workspacePath))
	r.Register(NewCodegraphFiles(workspacePath))
	return r
}
