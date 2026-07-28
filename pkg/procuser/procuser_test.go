package procuser

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConfiguredPrefersEnv(t *testing.T) {
	t.Setenv(EnvVar, "from-env")
	if got := Configured("from-settings"); got != "from-env" {
		t.Errorf("Configured = %q, want from-env", got)
	}
	t.Setenv(EnvVar, "")
	if got := Configured("from-settings"); got != "from-settings" {
		t.Errorf("Configured = %q, want from-settings", got)
	}
	if got := Configured(""); got != "" {
		t.Errorf("Configured = %q, want empty (run as self)", got)
	}
}

// The default (no service user) must never touch the command — every dev
// machine and the whole test suite depend on children running as-is.
func TestResolveEmptyIsNil(t *testing.T) {
	u, err := Resolve("")
	if err != nil || u != nil {
		t.Fatalf("Resolve(\"\") = (%v, %v), want (nil, nil)", u, err)
	}
	cmd := exec.Command("true")
	u.Apply(cmd)
	if cmd.SysProcAttr != nil || cmd.Env != nil {
		t.Error("Apply on a nil user must leave the command untouched")
	}
}

// A configured-but-missing account is a hard error: silently running as self
// would leave an operator believing the backend is isolated when it is not.
func TestResolveMissingUserErrors(t *testing.T) {
	if _, err := Resolve("headcount1-no-such-account-xyz"); err == nil {
		t.Error("expected an error for an unknown account")
	}
}

func TestEnsureOwnedCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hindsight")
	var u *User // nil: unconfigured
	if err := u.EnsureOwned(dir, 0o750); err != nil {
		t.Fatalf("EnsureOwned: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
}
