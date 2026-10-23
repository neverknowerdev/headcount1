package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-orchestrator/pkg/secrets/redact"
)

// TestUseButNotSee is the end-to-end contract of environment secrets at the
// tool layer: the agent's shell can USE the secret (compare it, send it),
// but the value never comes back into the tool output the LLM sees — the
// dispatch layer redacts it to [REDACTED:...].
func TestUseButNotSee(t *testing.T) {
	const secret = "env-secret-use-not-see-12345"
	redact.Register("environment-secret", secret)

	ws := t.TempDir()
	reg := DefaultRegistryWithEnv(ws, map[string]string{"MY_API_KEY": secret})

	// USE: the shell sees the real value.
	args, _ := json.Marshal(map[string]string{
		"command": `[ "$MY_API_KEY" = "` + secret + `" ] && echo USABLE`,
	})
	out, err := reg.Execute(context.Background(), "bash", args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "USABLE") {
		t.Fatalf("secret not usable in the shell: %q", out)
	}

	// NOT SEE: echoing it back yields only the redaction marker.
	args, _ = json.Marshal(map[string]string{"command": `echo "leak: $MY_API_KEY"`})
	out, err = reg.Execute(context.Background(), "bash", args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("secret leaked into tool output: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:environment-secret]") {
		t.Fatalf("expected redaction marker in output: %q", out)
	}

	// NOT SEE via env dump either.
	args, _ = json.Marshal(map[string]string{"command": `env | grep MY_API_KEY || true`})
	out, err = reg.Execute(context.Background(), "bash", args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("secret leaked via env dump: %q", out)
	}
}
