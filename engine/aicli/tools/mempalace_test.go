package tools

import (
	"encoding/json"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
)

func testScope() MemoryScope {
	return MemoryScope{
		ProjectWing: "project-webapp",
		CompanyWing: "company",
		AgentWing:   "agent-cto",
		Closet:      "task-dec-1",
		TaskPath:    "tasks/dec-1",
		RunID:       3,
		AddedBy:     "CTO",
		TaskEntity:  "task-dec-1",
	}
}

func TestMempalaceProxyRegistersCatalog(t *testing.T) {
	p := NewMempalaceProxy(db.MCPServer{Name: "mempalace-test"}, testScope(), nil)
	r := aicli.NewRegistry()
	summary := p.RegisterAll(r)

	want := []string{"recall_memory", "recall_run", "remember", "memory_facts", "memory_invalidate", "recall_company"}
	defs := r.Defs()
	byName := map[string]bool{}
	for _, d := range defs {
		byName[d.Function.Name] = true
		// Every tool must carry a valid JSON schema.
		var schema map[string]any
		if err := json.Unmarshal(d.Function.Parameters, &schema); err != nil {
			t.Errorf("tool %s has invalid parameters JSON: %v", d.Function.Name, err)
		}
	}
	for _, name := range want {
		if !byName[name] {
			t.Errorf("tool %s not registered (summary: %s)", name, summary)
		}
	}
}

func TestMemoryScopeWingFallback(t *testing.T) {
	s := MemoryScope{CompanyWing: "company"}
	if s.wing() != "company" {
		t.Errorf("wing = %q", s.wing())
	}
	s.ProjectWing = "project-x"
	if s.wing() != "project-x" {
		t.Errorf("wing = %q", s.wing())
	}
}

func TestMemoryScopeSourceFile(t *testing.T) {
	s := testScope()
	if got := s.sourceFile(5); got != "tasks/dec-1/run-5" {
		t.Errorf("sourceFile(5) = %q", got)
	}
	if got := s.sourceFile(0); got != "tasks/dec-1" {
		t.Errorf("sourceFile(0) = %q", got)
	}
	if got := (MemoryScope{}).sourceFile(1); got != "" {
		t.Errorf("empty scope sourceFile = %q", got)
	}
}

func TestIsDuplicate(t *testing.T) {
	if !isDuplicate(`{"duplicate": true, "similarity": 0.98}`) {
		t.Error("expected duplicate=true to be detected")
	}
	if isDuplicate(`{"duplicate": false}`) {
		t.Error("expected duplicate=false to pass")
	}
	if isDuplicate(`not json at all`) {
		t.Error("unparseable response must not be treated as duplicate")
	}
}

func TestCountResults(t *testing.T) {
	out := `{"query":"x","results":[{"text":"a"},{"text":"b"}]}`
	if n := countResults(out); n != 2 {
		t.Errorf("countResults = %d", n)
	}
	if n := countResults("garbage"); n != 0 {
		t.Errorf("countResults(garbage) = %d", n)
	}
}
