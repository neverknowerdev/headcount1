package aicli_test

import (
	"os"
	"testing"

	"agent-orchestrator/engine/aicli/tools"
)

// TestMain lets this test binary act as the sandbox re-exec child: tests in
// this package run the bash tool, which re-executes os.Executable() — i.e.
// this very binary — with the sandbox child marker (Linux only; a no-op
// elsewhere and on normal test runs).
func TestMain(m *testing.M) {
	tools.MaybeRunSandboxChild()
	os.Exit(m.Run())
}
