package tools

import (
	"os"
	"testing"
)

// TestMain lets this test binary act as the sandbox re-exec child: the exec
// tests below run the bash tool, which re-executes os.Executable() — i.e.
// this very binary — with the sandbox child marker (Linux only; a no-op
// elsewhere and on normal test runs).
func TestMain(m *testing.M) {
	MaybeRunSandboxChild()
	os.Exit(m.Run())
}
