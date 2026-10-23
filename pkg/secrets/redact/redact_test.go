package redact

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestRegisteredSecretExactMatch(t *testing.T) {
	resetForTest()
	Register("provider-api-key", "sUpErSeCrEtValue123")

	got := Scrub("the key is sUpErSeCrEtValue123, don't tell")
	if strings.Contains(got, "sUpErSeCrEtValue123") {
		t.Fatalf("secret survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:provider-api-key]") {
		t.Fatalf("marker missing: %q", got)
	}
}

func TestRegisteredSecretEncodedForms(t *testing.T) {
	resetForTest()
	secret := "tok-abc123XYZ/45+very=secret"
	Register("mcp-token", secret)

	cases := map[string]string{
		"base64std":     base64.StdEncoding.EncodeToString([]byte(secret)),
		"base64std-raw": strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(secret)), "="),
		"base64url":     base64.URLEncoding.EncodeToString([]byte(secret)),
		"urlescaped":    url.QueryEscape(secret),
	}
	for name, form := range cases {
		got := Scrub("prefix " + form + " suffix")
		if strings.Contains(got, form) {
			t.Errorf("%s form survived: %q", name, got)
		}
	}
}

func TestRegisteredSecretJSONEscaped(t *testing.T) {
	resetForTest()
	secret := "line1\nline2\t\"quoted\""
	Register("ssh-key", secret)

	asJSON := `{"content":"line1\nline2\t\"quoted\""}`
	got := Scrub(asJSON)
	if strings.Contains(got, `line1\nline2`) {
		t.Fatalf("JSON-escaped form survived: %q", got)
	}
}

func TestShortSecretsNotRegistered(t *testing.T) {
	resetForTest()
	Register("tiny", "abc")
	got := Scrub("abc is fine in prose")
	if got != "abc is fine in prose" {
		t.Fatalf("short value should not be redacted: %q", got)
	}
}

func TestScrubIdempotent(t *testing.T) {
	resetForTest()
	Register("k", "sUpErSeCrEtValue123")
	once := Scrub("x sUpErSeCrEtValue123 y")
	twice := Scrub(once)
	if once != twice {
		t.Fatalf("not idempotent: %q vs %q", once, twice)
	}
}

func TestPEMBlockRedacted(t *testing.T) {
	resetForTest()
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\nmore==\n-----END OPENSSH PRIVATE KEY-----"
	got := Scrub("before\n" + pem + "\nafter")
	if strings.Contains(got, "b3BlbnNzaC1rZXktdjE") {
		t.Fatalf("PEM body survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:private-key]") {
		t.Fatalf("marker missing: %q", got)
	}
}

func TestTokenPatterns(t *testing.T) {
	resetForTest()
	cases := []struct{ name, in, mustNotContain string }{
		{"openai", "key=sk-abcdefghijklmnopqrstuvwx12", "sk-abcdefghijklmnopqrstuvwx12"},
		{"github-pat", "ghp_" + strings.Repeat("A", 36), "ghp_" + strings.Repeat("A", 36)},
		{"github-fine", "github_pat_" + strings.Repeat("a", 22) + "_" + strings.Repeat("b", 59), "github_pat_"},
		{"aws", "AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"slack", "xoxb-1234567890-abcdefghij", "xoxb-1234567890"},
		{"google", "AIza" + strings.Repeat("a", 35), "AIza" + strings.Repeat("a", 35)},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk", "eyJhbGciOiJIUzI1NiJ9."},
	}
	for _, c := range cases {
		got := Scrub(c.in)
		if strings.Contains(got, c.mustNotContain) {
			t.Errorf("%s: token survived: %q", c.name, got)
		}
	}
}

func TestAuthorizationHeaderRedacted(t *testing.T) {
	resetForTest()
	got := Scrub("Authorization: Bearer abcdef123456789.token-value")
	if strings.Contains(got, "abcdef123456789") {
		t.Fatalf("bearer token survived: %q", got)
	}
	if !strings.Contains(got, "Authorization: Bearer ") {
		t.Fatalf("header context should remain: %q", got)
	}
}

func TestConnectionURLPasswordRedacted(t *testing.T) {
	resetForTest()
	got := Scrub("DATABASE_URL=postgres://app:hunter22secret@db:5432/x?sslmode=disable")
	if strings.Contains(got, "hunter22secret") {
		t.Fatalf("url password survived: %q", got)
	}
	if !strings.Contains(got, "postgres://app:") {
		t.Fatalf("user/scheme should remain: %q", got)
	}
}

func TestEnvFileAssignmentsRedacted(t *testing.T) {
	resetForTest()
	in := "PORT=8080\nOPENAI_API_KEY=sk_live_notactuallyshaped\nexport GITHUB_TOKEN=\"tok_value_12345\"\nDB_PASSWORD='p4ssw0rd!!'\n"
	got := Scrub(in)
	for _, leaked := range []string{"sk_live_notactuallyshaped", "tok_value_12345"} {
		if strings.Contains(got, leaked) {
			t.Errorf("env value survived: %q in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "PORT=8080") {
		t.Errorf("non-secret env var should remain: %q", got)
	}
}

// Precision: ordinary content must pass through untouched.
func TestNoFalsePositives(t *testing.T) {
	resetForTest()
	benign := []string{
		"the quick brown fox jumps over the lazy dog",
		"commit 3f9d2c1a7b8e4d5f6a0b1c2d3e4f5a6b7c8d9e0f fixed the bug",
		"uuid 550e8400-e29b-41d4-a716-446655440000",
		"func getAPIKey() string { return os.Getenv(\"OPENAI_API_KEY\") }",
		"const KEY = \"cache\"", // KEY-named var but short non-secret value
		"https://example.com/path?q=hello",
		"user@example.com wrote a task",
		"risk-assessment: medium", // sk- inside a word must not trigger
	}
	for _, s := range benign {
		if got := Scrub(s); got != s {
			t.Errorf("false positive: %q → %q", s, got)
		}
	}
}

func TestScrubEntryNested(t *testing.T) {
	resetForTest()
	Register("k", "sUpErSeCrEtValue123")
	entry := map[string]interface{}{
		"content": "sUpErSeCrEtValue123",
		"extra": map[string]interface{}{
			"deep": []interface{}{"x sUpErSeCrEtValue123", 42},
		},
		"seq": int64(7),
	}
	ScrubEntry(entry)
	if strings.Contains(entry["content"].(string), "sUpEr") {
		t.Fatalf("content not scrubbed")
	}
	deep := entry["extra"].(map[string]interface{})["deep"].([]interface{})
	if strings.Contains(deep[0].(string), "sUpEr") {
		t.Fatalf("nested slice not scrubbed")
	}
	if entry["seq"].(int64) != 7 {
		t.Fatalf("non-string values must be untouched")
	}
}

func TestConcurrentRegisterAndScrub(t *testing.T) {
	resetForTest()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			Register("k", "concurrent-secret-value-abc123")
		}
	}()
	for i := 0; i < 200; i++ {
		_ = Scrub("text concurrent-secret-value-abc123 more")
	}
	<-done
	if got := Scrub("concurrent-secret-value-abc123"); strings.Contains(got, "abc123") {
		t.Fatalf("secret survived after concurrent registration: %q", got)
	}
}
