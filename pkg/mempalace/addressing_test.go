package mempalace

import (
	"regexp"
	"testing"

	"agent-orchestrator/db"
)

// safeNameRe mirrors mempalace's server-side wing/room validation
// (_SAFE_NAME_RE in mempalace/config.py). Every name this package produces
// must match it, or MCP calls fail with "wing contains invalid characters".
var safeNameRe = regexp.MustCompile(`^(?:[a-zA-Z0-9]|[a-zA-Z0-9][\w .'-]{0,126}[a-zA-Z0-9])$`)

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"My Project":        "my-project",
		"acme":              "acme",
		"ACME Corp!":        "acme-corp",
		"a":                 "a",
		"":                  "unnamed",
		"--weird--":         "weird",
		"proj:with/slashes": "proj-with-slashes",
		"DEC-42":            "dec-42",
		"тест проект":       "unnamed", // non-ASCII collapses away
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveProducesValidNames(t *testing.T) {
	company := db.Company{Name: "Acme Inc", ShortName: "ACME"}
	project := db.Project{Name: "Web App 2.0"}
	task := db.Task{ID: 7, RefKey: "ACM-7"}

	addr := Resolve(company, &project, &task, 3, "CTO Agent")

	for name, v := range map[string]string{
		"wing":       addr.Wing,
		"agent wing": addr.AgentWing,
		"room":       addr.Room,
		"closet":     addr.Closet,
	} {
		if !safeNameRe.MatchString(v) {
			t.Errorf("%s %q does not match mempalace safe-name charset", name, v)
		}
	}

	if addr.Wing != "project-web-app-2-0" {
		t.Errorf("wing = %q", addr.Wing)
	}
	if addr.AgentWing != "agent-cto-agent" {
		t.Errorf("agent wing = %q", addr.AgentWing)
	}
	if addr.Closet != "task-acm-7" {
		t.Errorf("closet = %q", addr.Closet)
	}
	if addr.SourceFile != "tasks/acm-7/run-3" {
		t.Errorf("source file = %q", addr.SourceFile)
	}
}

func TestResolveWithoutProjectFallsBackToCompanyWing(t *testing.T) {
	addr := Resolve(db.Company{ShortName: "x"}, nil, nil, 0, "")
	if addr.Wing != CompanyWing {
		t.Errorf("wing = %q, want %q", addr.Wing, CompanyWing)
	}
	if addr.Closet != "" || addr.SourceFile != "" {
		t.Errorf("expected empty closet/source file, got %q/%q", addr.Closet, addr.SourceFile)
	}
}

func TestTaskClosetFallsBackToID(t *testing.T) {
	if got := TaskCloset(db.Task{ID: 12}); got != "task-12" {
		t.Errorf("closet = %q", got)
	}
}
