package hindsight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsMigrationFailure(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"alembic unknown revision", "ERROR - hindsight_api.migrations - Failed to run database migrations: Can't locate revision identified by 'e7c3a9f1b2d5'", true},
		{"runtime error wrapper", "RuntimeError: Database migration failed", true},
		{"alembic command error", "alembic.util.exc.CommandError: Can't locate revision", true},
		{"resolution error", "alembic.script.revision.ResolutionError: No such revision or branch 'abc'", true},
		{"unrelated crash", "OSError: address already in use", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := isMigrationFailure(c.output); got != c.want {
			t.Errorf("%s: isMigrationFailure = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSummarizeFailure(t *testing.T) {
	out := `2026-07-13 15:54:48 ERROR hindsight_api.migrations - Failed to run database migrations: Can't locate revision identified by 'e7c3a9f1b2d5'
RuntimeError: Database migration failed`
	got := summarizeFailure("hindsight-api exited during startup", out)
	if !strings.Contains(got, "Can't locate revision identified by 'e7c3a9f1b2d5'") {
		t.Errorf("expected the revision line in the summary, got %q", got)
	}
	if len(got) > 400 {
		t.Errorf("summary too long for a UI banner: %d chars", len(got))
	}

	// Migration failure without a revision line still gets a stable message.
	got = summarizeFailure("exited", "RuntimeError: Database migration failed")
	if !strings.Contains(got, "database migration failed") {
		t.Errorf("expected generic migration message, got %q", got)
	}

	// Non-migration failures fall through to the error itself.
	got = summarizeFailure("hindsight at http://127.0.0.1:8888 not healthy after 5m0s", "some unrelated output")
	if !strings.Contains(got, "not healthy after") {
		t.Errorf("expected passthrough error, got %q", got)
	}
}

func TestFallbackSchemaName(t *testing.T) {
	cases := map[string]string{
		"0.6.1":    "mem_v0_6_1",
		"0.8.4rc1": "mem_v0_8_4rc1",
		"":         "mem_fallback",
	}
	for version, want := range cases {
		if got := fallbackSchemaName(version); got != want {
			t.Errorf("fallbackSchemaName(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestSchemaStateRoundTrip(t *testing.T) {
	t.Setenv("E2E_PAPERCLIP_HOME", t.TempDir())

	// No state file → not found, resolveSchema falls back to public.
	if _, ok := loadSchemaState(); ok {
		t.Fatal("expected no schema state in a fresh home")
	}
	if got := resolveSchema(); got != "public" {
		t.Errorf("resolveSchema with no state = %q, want public", got)
	}

	st := schemaState{
		Schema:           "mem_v0_6_1",
		PreviousSchema:   "public",
		Reason:           "incompatible migrations",
		HindsightVersion: "0.6.1",
		SwitchedAt:       "2026-07-14T09:00:00Z",
	}
	if err := saveSchemaState(st); err != nil {
		t.Fatalf("saveSchemaState: %v", err)
	}
	loaded, ok := loadSchemaState()
	if !ok {
		t.Fatal("expected schema state after save")
	}
	if loaded != st {
		t.Errorf("round-trip mismatch: %+v != %+v", loaded, st)
	}
	if got := resolveSchema(); got != "mem_v0_6_1" {
		t.Errorf("resolveSchema = %q, want mem_v0_6_1", got)
	}

	// Env override always wins over persisted state.
	t.Setenv("HINDSIGHT_API_DATABASE_SCHEMA", "custom")
	if got := resolveSchema(); got != "custom" {
		t.Errorf("resolveSchema with env override = %q, want custom", got)
	}

	// A corrupt state file is treated as absent, not fatal.
	t.Setenv("HINDSIGHT_API_DATABASE_SCHEMA", "")
	os.Setenv("HINDSIGHT_API_DATABASE_SCHEMA", "")
	os.Unsetenv("HINDSIGHT_API_DATABASE_SCHEMA")
	if err := os.WriteFile(schemaStatePath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSchemaState(); ok {
		t.Error("corrupt state file must read as absent")
	}
	if got := resolveSchema(); got != "public" {
		t.Errorf("resolveSchema with corrupt state = %q, want public", got)
	}
}

func TestInstalledVersionFromDistInfo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("E2E_PAPERCLIP_HOME", home)

	// No venv → empty.
	if got := installedVersion(); got != "" {
		t.Errorf("installedVersion in empty home = %q, want empty", got)
	}

	distInfo := filepath.Join(home, ".paperclip2", "venv", "lib", "python3.14", "site-packages", "hindsight_api-0.6.1.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installedVersion(); got != "0.6.1" {
		t.Errorf("installedVersion = %q, want 0.6.1", got)
	}
}

func TestTailBufferKeepsOnlyTail(t *testing.T) {
	tb := newTailBuffer(8)
	tb.Write([]byte("0123456789abcdef"))
	if got := tb.String(); got != "89abcdef" {
		t.Errorf("tail = %q, want 89abcdef", got)
	}
}
