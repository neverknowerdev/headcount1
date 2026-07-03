//go:build !linux

package tools

// MaybeRunSandboxChild is a no-op outside Linux; the Landlock re-exec trick
// (see sandbox_exec_linux.go) is only used there. macOS wraps the command
// with sandbox-exec instead, which needs no re-exec hook.
func MaybeRunSandboxChild() {}
