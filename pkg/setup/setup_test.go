package setup

import "testing"

func TestParseFailures(t *testing.T) {
	output := `[setup] git: OK
[setup] python3: OK
[setup] pip: OK
[setup] markitdown: not found — installing...
[setup] WARNING: markitdown — pip install failed — web_fetch markdown conversion will be unavailable
[setup] npm: OK
[setup] chromium: OK (/usr/bin/chromium)
[setup] gh CLI: OK
[setup] codegraph: OK

[setup] DETAIL_BEGIN markitdown
ERROR: Could not find a version that satisfies the requirement markitdown
ERROR: No matching distribution found for markitdown
[setup] DETAIL_END

[setup] Some dependencies are missing or could not be installed:
  • markitdown: pip install failed — web_fetch markdown conversion will be unavailable
`

	failures := parseFailures(output)
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %+v", len(failures), failures)
	}
	f := failures[0]
	if f.Name != "markitdown" {
		t.Errorf("Name = %q, want %q", f.Name, "markitdown")
	}
	if f.Reason != "pip install failed — web_fetch markdown conversion will be unavailable" {
		t.Errorf("Reason = %q", f.Reason)
	}
	wantDetail := "ERROR: Could not find a version that satisfies the requirement markitdown\nERROR: No matching distribution found for markitdown"
	if f.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", f.Detail, wantDetail)
	}
}

func TestParseFailures_MultipleDeps(t *testing.T) {
	output := `[setup] DETAIL_BEGIN git
git install failed: no package manager
[setup] DETAIL_END

[setup] DETAIL_BEGIN markitdown
pip error
[setup] DETAIL_END

[setup] Some dependencies are missing or could not be installed:
  • git: not found and could not be installed — install it manually
  • markitdown: pip install failed — web_fetch markdown conversion will be unavailable
`
	failures := parseFailures(output)
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures, got %d: %+v", len(failures), failures)
	}
	if failures[0].Name != "git" || failures[0].Detail != "git install failed: no package manager" {
		t.Errorf("unexpected first failure: %+v", failures[0])
	}
	if failures[1].Name != "markitdown" || failures[1].Detail != "pip error" {
		t.Errorf("unexpected second failure: %+v", failures[1])
	}
}

func TestParseFailures_NoFailures(t *testing.T) {
	output := "[setup] All dependencies OK\n"
	if failures := parseFailures(output); failures != nil {
		t.Errorf("expected nil failures, got %+v", failures)
	}
}

func TestParseFailures_MultiWordName(t *testing.T) {
	// A dependency whose display name contains a space (e.g. "gh CLI") should
	// still round-trip through the underscore-joined detail id.
	output := `[setup] DETAIL_BEGIN gh_CLI
some raw install output
[setup] DETAIL_END

[setup] Some dependencies are missing or could not be installed:
  • gh CLI: could not be installed
`
	failures := parseFailures(output)
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(failures))
	}
	if failures[0].Name != "gh CLI" {
		t.Errorf("Name = %q", failures[0].Name)
	}
	if failures[0].Detail != "some raw install output" {
		t.Errorf("Detail = %q", failures[0].Detail)
	}
}

func TestParseSoftFailures(t *testing.T) {
	output := `[setup] gh CLI: not found — installing...
[setup] SOFT_FAIL: gh CLI — could not be installed — install manually from https://cli.github.com for GitHub MCP server support

[setup] DETAIL_BEGIN gh_CLI
curl: (6) Could not resolve host: cli.github.com
[setup] DETAIL_END

[setup] Some optional dependencies are missing or could not be installed:
  • gh CLI: could not be installed — install manually from https://cli.github.com for GitHub MCP server support
`
	warnings := parseSoftFailures(output)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warnings), warnings)
	}
	w := warnings[0]
	if w.Name != "gh CLI" {
		t.Errorf("Name = %q", w.Name)
	}
	if w.Reason != "could not be installed — install manually from https://cli.github.com for GitHub MCP server support" {
		t.Errorf("Reason = %q", w.Reason)
	}
	if w.Detail != "curl: (6) Could not resolve host: cli.github.com" {
		t.Errorf("Detail = %q", w.Detail)
	}
}

func TestParseSoftFailures_DoesNotLeakIntoHardFailures(t *testing.T) {
	output := `[setup] SOFT_FAIL: gh CLI — could not be installed

[setup] All dependencies OK
`
	if failures := parseFailures(output); failures != nil {
		t.Errorf("soft failure leaked into hard failures: %+v", failures)
	}
}
