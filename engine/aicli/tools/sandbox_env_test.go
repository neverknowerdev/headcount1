package tools

import (
	"strings"
	"testing"
)

func TestScrubbedEnvRemovesServerSecrets(t *testing.T) {
	t.Setenv("HEADCOUNT1_MASTER_KEY", "super-secret")
	t.Setenv("HEADCOUNT1_BOOT_KEY", "boot-secret")
	t.Setenv("VAULT_TOKEN", "vault-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("DATABASE_URL", "postgres://user:pw@host/db")
	t.Setenv("SMTP_PASSWORD", "mail-secret")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("MY_PROJECT_VAR", "keep-me")

	env := scrubbedEnv()
	joined := strings.Join(env, "\n")

	for _, leaked := range []string{"super-secret", "boot-secret", "vault-secret", "aws-secret", "postgres://", "mail-secret"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("scrubbed env leaked a server secret: %q", leaked)
		}
	}
	// Ordinary vars the agent needs must survive.
	if !strings.Contains(joined, "PATH=") {
		t.Error("PATH must be preserved for the agent shell")
	}
	if !strings.Contains(joined, "MY_PROJECT_VAR=keep-me") {
		t.Error("non-secret project vars must be preserved")
	}
}
