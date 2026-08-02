package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestExecCommandExtraEnv: injected environment secrets are visible to the
// child process as env vars (the "use" half of use-but-not-see; the "not see"
// half — redaction of the tool output — is layered on in Registry.Execute and
// tested in engine/aicli).
func TestExecCommandExtraEnv(t *testing.T) {
	ws := t.TempDir()
	exec := NewExecCommand(ws)
	exec.SetExtraEnv(map[string]string{"E2E_INJECTED_SECRET": "the-injected-value"})

	args, _ := json.Marshal(map[string]string{
		"command": `[ "$E2E_INJECTED_SECRET" = "the-injected-value" ] && echo MATCH || echo NOMATCH`,
	})
	out, err := exec.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "MATCH") || strings.Contains(out, "NOMATCH") {
		t.Fatalf("injected env var not visible to the shell: %q", out)
	}
}

// TestExecCommandExtraEnvOverridesInherited: a deliberately injected secret
// wins over a same-named var inherited from the server environment.
func TestExecCommandExtraEnvOverridesInherited(t *testing.T) {
	t.Setenv("E2E_COLLIDING_VAR", "from-server")
	ws := t.TempDir()
	exec := NewExecCommand(ws)
	exec.SetExtraEnv(map[string]string{"E2E_COLLIDING_VAR": "from-environment"})

	args, _ := json.Marshal(map[string]string{"command": `echo "got=$E2E_COLLIDING_VAR"`})
	out, err := exec.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "got=from-environment") {
		t.Fatalf("injected env should win: %q", out)
	}
}
