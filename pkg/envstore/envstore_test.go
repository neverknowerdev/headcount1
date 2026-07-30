package envstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// isolate points the store at a temp dir. Path() resolves through
// db.Headcount1Home(), which honours E2E_HEADCOUNT1_HOME.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", home)
	return filepath.Join(home, ".headcount1")
}

func TestValidateAcceptsOrdinaryConfig(t *testing.T) {
	for _, key := range []string{
		"DATABASE_URL", "SMTP_HOST", "SMTP_PASSWORD", "APP_BASE_URL", "PORT",
		"VAULT_ADDR", "VAULT_TOKEN", "SESSION_REAUTH_GAP", "HEADCOUNT1_BOOT_KEY", "X",
	} {
		require.NoError(t, Validate(key), "should accept %s", key)
	}
}

// TestValidateRefusesCodeExecution is the security core of this package: every
// key here would turn a delivered value into code the server runs, or would
// relax the controls that keep the deploy key from doing so.
func TestValidateRefusesCodeExecution(t *testing.T) {
	for _, key := range []string{
		// Loaders and interpreters run code straight out of these.
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "DYLD_INSERT_LIBRARIES",
		"PATH", "SHELL", "BASH_ENV", "ENV", "IFS", "CDPATH",
		"NODE_OPTIONS", "PYTHONPATH", "PYTHONSTARTUP", "PERL5OPT", "RUBYOPT",
		"GIT_SSH_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_PAGER",
		// Redirecting the server's trust or traffic.
		"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"SSH_AUTH_SOCK", "HOME",
		// The deploy system's own guardrails: a leaked key must not be able to
		// widen what the next deploy is allowed to fetch, or rotate the key.
		"HEADCOUNT1_DEPLOY_ALLOWED_HOSTS", "HEADCOUNT1_DEPLOY_REPO",
		"HEADCOUNT1_DEPLOY_API_KEY", "HEADCOUNT1_ENV",
		// Security switches and test-mode escapes.
		"HEADCOUNT1_ENABLE_GLOBAL_ADMIN_API", "HEADCOUNT1_SANDBOX_READ_SCOPING",
		"HEADCOUNT1_GIT_STRICT_HOST_KEY_CHECKING", "E2E_MODE", "E2E_HEADCOUNT1_HOME", "RECORD",
	} {
		require.Error(t, Validate(key), "must refuse %s", key)
	}
}

// TestValidateRejectsMalformedKeys covers the lowercase rule specifically: Go's
// proxy resolution and the dynamic loader honour lowercase spellings, so if
// lowercase were accepted the denylist would need a second entry per name.
func TestValidateRejectsMalformedKeys(t *testing.T) {
	for _, key := range []string{
		"https_proxy", "ld_preload", "path", // lowercase forms of denied names
		"", "1STARTS_WITH_DIGIT", "HAS-DASH", "HAS SPACE", "HAS=EQUALS",
		"HAS\nNEWLINE", "lower_case", "MiXeD",
		strings.Repeat("A", 200),
	} {
		require.Error(t, Validate(key), "must reject %q", key)
	}
}

// TestPartitionKeepsGoodKeysAndExplainsTheRest pins the "deliver everything"
// contract: CI ships every variable and secret the environment holds, so hitting
// an unusable name is routine. The usable ones must still land, and the rest must
// come back with a reason instead of failing the deploy.
func TestPartitionKeepsGoodKeysAndExplainsTheRest(t *testing.T) {
	accepted, skipped := Partition(map[string]string{
		"DATABASE_URL": "postgres://ok",
		"SMTP_HOST":    "mail",
		"PATH":         "/tmp/evil",
		"GIT_TOKEN":    "a repo secret for some other workflow",
		"lower":        "x",
		"BIG":          strings.Repeat("x", MaxValueBytes+1),
	})

	require.Equal(t, map[string]string{"DATABASE_URL": "postgres://ok", "SMTP_HOST": "mail"}, accepted,
		"one unusable name must not cost the deploy its real configuration")

	summary := strings.Join(SkippedSummary(skipped), "\n")
	require.Len(t, skipped, 4)
	require.Contains(t, summary, "PATH: refused:")
	require.Contains(t, summary, "GIT_TOKEN: refused:")
	require.Contains(t, summary, "lower: not a valid environment variable name")
	require.Contains(t, summary, "BIG: value is")

	// Nothing to complain about, and nothing delivered at all.
	accepted, skipped = Partition(map[string]string{"DATABASE_URL": "x"})
	require.Len(t, accepted, 1)
	require.Empty(t, skipped)

	accepted, skipped = Partition(nil)
	require.Empty(t, accepted)
	require.Empty(t, skipped)
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	dir := isolate(t)

	in := Store{
		Values:     map[string]string{"DATABASE_URL": "postgres://x", "SMTP_HOST": "mail"},
		SecretKeys: []string{"DATABASE_URL"},
	}
	require.NoError(t, Save(in))

	// Secrets sit in this file in the clear, so it must not be group/world
	// readable — same requirement as a systemd EnvironmentFile.
	info, err := os.Stat(filepath.Join(dir, "deploy-env.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	out, err := Load()
	require.NoError(t, err)
	require.Equal(t, in.Values, out.Values)
	require.Equal(t, in.SecretKeys, out.SecretKeys)
	require.NotEmpty(t, out.UpdatedAt, "Save stamps when the config landed")
	require.Equal(t, []string{"DATABASE_URL", "SMTP_HOST"}, out.Keys())
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	isolate(t)
	s, err := Load()
	require.NoError(t, err, "no deploy has delivered config yet — not a failure")
	require.Empty(t, s.Values)
}

func TestLoadCorruptFileReportsError(t *testing.T) {
	dir := isolate(t)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "deploy-env.json"), []byte("{not json"), 0600))
	_, err := Load()
	require.Error(t, err, "a corrupt store must be reported, not silently treated as empty")
}

func TestApplySetsEnvironmentAndOverridesInherited(t *testing.T) {
	isolate(t)
	// An inherited value that the delivered configuration must win over: the
	// GitHub Environment is the source of truth, so a stale systemd value must
	// not shadow it.
	t.Setenv("SMTP_HOST", "stale-from-systemd")
	t.Setenv("UNTOUCHED_VAR", "keep-me")

	require.NoError(t, Save(Store{Values: map[string]string{
		"SMTP_HOST": "mail.example.com",
		"NEW_VAR":   "fresh",
	}}))

	applied, err := Apply()
	require.NoError(t, err)
	require.Equal(t, []string{"NEW_VAR", "SMTP_HOST"}, applied)
	require.Equal(t, "mail.example.com", os.Getenv("SMTP_HOST"))
	require.Equal(t, "fresh", os.Getenv("NEW_VAR"))
	require.Equal(t, "keep-me", os.Getenv("UNTOUCHED_VAR"), "unrelated vars are left alone")

	// Applying nothing is fine and touches nothing.
	os.Unsetenv("NEW_VAR")
}

// TestApplyRefusesTamperedStore is the second half of the denylist: receipt-time
// validation only helps if the file on disk is re-checked, since the file could
// be edited by hand or written by an older, more permissive build.
func TestApplyRefusesTamperedStore(t *testing.T) {
	dir := isolate(t)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "deploy-env.json"),
		[]byte(`{"values":{"LD_PRELOAD":"/tmp/evil.so"}}`), 0600))

	before := os.Getenv("LD_PRELOAD")
	_, err := Apply()
	require.Error(t, err)
	require.ErrorContains(t, err, "LD_PRELOAD")
	require.Equal(t, before, os.Getenv("LD_PRELOAD"), "the refused key must not have been set")
}

func TestDigestDetectsAnyChange(t *testing.T) {
	base := Store{Values: map[string]string{"A": "1", "B": "2"}}

	require.Equal(t, base.Digest(), Store{Values: map[string]string{"B": "2", "A": "1"}}.Digest(),
		"map order must not matter")
	require.NotEqual(t, base.Digest(), Store{Values: map[string]string{"A": "1", "B": "3"}}.Digest(),
		"a changed value must change the digest — that is what triggers a restart")
	require.NotEqual(t, base.Digest(), Store{Values: map[string]string{"A": "1"}}.Digest(),
		"a removed key must change the digest")
	require.Equal(t, Store{}.Digest(), Store{Values: map[string]string{}}.Digest(),
		"nil and empty both mean nothing delivered")

	// Length-prefixing: these two differ only in where the boundary between a
	// name and a value falls, and must not collide.
	require.NotEqual(t,
		Store{Values: map[string]string{"AB": "C"}}.Digest(),
		Store{Values: map[string]string{"A": "BC"}}.Digest())

	// SecretKeys is metadata about the same values, not part of the identity.
	require.Equal(t, base.Digest(), Store{
		Values: map[string]string{"A": "1", "B": "2"}, SecretKeys: []string{"A"},
	}.Digest())
}
