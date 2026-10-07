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
	return DefaultRegistryWithEnv(workspacePath, nil, readOnlyDirs...)
}

// DefaultRegistryWithEnv is DefaultRegistry with extra env vars injected into
// the shell tool — the task's environment secrets. The agent can reference
// them by name ($API_KEY); the values never appear in its message history
// (tool output redaction upstream).
func DefaultRegistryWithEnv(workspacePath string, extraEnv map[string]string, readOnlyDirs ...string) *aicli.Registry {
	r := aicli.NewRegistry()
	r.Register(NewReadFile(workspacePath, readOnlyDirs...))
	r.Register(NewWriteFile(workspacePath))
	r.Register(NewListDir(workspacePath, readOnlyDirs...))
	exec := NewExecCommand(workspacePath, readOnlyDirs...)
	exec.SetExtraEnv(extraEnv)
	r.Register(exec)
	r.Register(NewGrep(workspacePath, readOnlyDirs...))
	r.Register(NewWebFetch())
	r.Register(NewBrowserUse())
	return r
}
