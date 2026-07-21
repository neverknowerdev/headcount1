package tools

import (
	"agent-orchestrator/engine/aicli"
)

// DefaultRegistry returns a Registry pre-loaded with the built-in file, shell,
// and web tools, all sandboxed to workspacePath.
// Codegraph tools are added separately via CodegraphProxy in the engine.
// readOnlyDirs are extra roots the read/ls/grep/bash tools may READ from
// (e.g. the parent task's workdir or the artifacts directory); writes stay
// restricted to workspacePath.
func DefaultRegistry(workspacePath string, readOnlyDirs ...string) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(NewReadFile(workspacePath, readOnlyDirs...))
	r.Register(NewWriteFile(workspacePath))
	r.Register(NewListDir(workspacePath, readOnlyDirs...))
	r.Register(NewExecCommand(workspacePath, readOnlyDirs...))
	r.Register(NewGrep(workspacePath, readOnlyDirs...))
	r.Register(NewWebFetch())
	r.Register(NewBrowserUse())
	return r
}
