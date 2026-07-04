package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"agent-orchestrator/db"
)

// fakeCall builds a callFn stub that answers per MCP tool name and records
// the calls it receives.
type fakeCall struct {
	responses map[string]string // tool → JSON response ("" → error)
	calls     []string
	args      map[string]map[string]any
}

func (f *fakeCall) fn(_ context.Context, _ db.MCPServer, tool string, args any) (string, error) {
	f.calls = append(f.calls, tool)
	if f.args == nil {
		f.args = map[string]map[string]any{}
	}
	b, _ := json.Marshal(args)
	var m map[string]any
	json.Unmarshal(b, &m)
	f.args[tool] = m
	resp, ok := f.responses[tool]
	if !ok || resp == "" {
		return "", fmt.Errorf("tool %s unavailable", tool)
	}
	return resp, nil
}

func newTestProxy(f *fakeCall) *MempalaceProxy {
	p := NewMempalaceProxy(db.MCPServer{Name: "mempalace-test"}, testScope(), nil)
	p.callFn = f.fn
	return p
}

func execTool(t *testing.T, p *MempalaceProxy, name string, args map[string]any) (string, error) {
	t.Helper()
	for _, spec := range mpCatalog {
		if spec.name == name {
			raw := map[string]json.RawMessage{}
			for k, v := range args {
				b, _ := json.Marshal(v)
				raw[k] = b
			}
			return spec.execute(p, context.Background(), raw)
		}
	}
	t.Fatalf("tool %s not in catalog", name)
	return "", nil
}

func TestCheckDuplicatePrimaryPath(t *testing.T) {
	f := &fakeCall{responses: map[string]string{
		"mempalace_check_duplicate": `{"is_duplicate": true, "matches": [{"content": "existing fact text", "similarity": 0.91}]}`,
	}}
	p := newTestProxy(f)

	dup, existing := p.CheckDuplicate(context.Background(), "some fact")
	if !dup || existing != "existing fact text" {
		t.Errorf("dup=%v existing=%q", dup, existing)
	}
	if th := f.args["mempalace_check_duplicate"]["threshold"]; th != rememberDupThreshold {
		t.Errorf("threshold = %v, want %v", th, rememberDupThreshold)
	}
	// Conclusive answer — no search fallback.
	for _, c := range f.calls {
		if c == "mempalace_search" {
			t.Error("search fallback must not run when check_duplicate is conclusive")
		}
	}
}

func TestCheckDuplicateNotADuplicate(t *testing.T) {
	f := &fakeCall{responses: map[string]string{
		"mempalace_check_duplicate": `{"is_duplicate": false, "matches": []}`,
	}}
	dup, _ := newTestProxy(f).CheckDuplicate(context.Background(), "fresh fact")
	if dup {
		t.Error("fresh content flagged as duplicate")
	}
}

func TestCheckDuplicateFallbackToSearch(t *testing.T) {
	// vector_disabled makes check_duplicate inconclusive → wing-scoped search.
	f := &fakeCall{responses: map[string]string{
		"mempalace_check_duplicate": `{"is_duplicate": false, "matches": [], "vector_disabled": true}`,
		"mempalace_search":          `{"results": [{"text": "already stored", "similarity": 0.9}]}`,
	}}
	p := newTestProxy(f)
	dup, existing := p.CheckDuplicate(context.Background(), "paraphrased fact")
	if !dup || existing != "already stored" {
		t.Errorf("dup=%v existing=%q — fallback should have caught it", dup, existing)
	}
	if wing := f.args["mempalace_search"]["wing"]; wing != "project-webapp" {
		t.Errorf("fallback search wing = %v, want scope wing", wing)
	}

	// Below-threshold top hit is not a duplicate.
	f.responses["mempalace_search"] = `{"results": [{"text": "vaguely related", "similarity": 0.6}]}`
	if dup, _ := p.CheckDuplicate(context.Background(), "different fact"); dup {
		t.Error("below-threshold similarity flagged as duplicate")
	}

	// Both paths erroring → not a duplicate (fail open: storing twice beats
	// dropping a fact).
	f2 := &fakeCall{responses: map[string]string{}}
	if dup, _ := newTestProxy(f2).CheckDuplicate(context.Background(), "x"); dup {
		t.Error("errors must not be treated as duplicates")
	}
}

func TestRememberReturnsExistingOnDuplicate(t *testing.T) {
	f := &fakeCall{responses: map[string]string{
		"mempalace_check_duplicate": `{"is_duplicate": true, "matches": [{"content": "No agent has shell tools — builds cannot run"}]}`,
	}}
	out, err := execTool(t, newTestProxy(f), "remember", map[string]any{
		"content": "PLATFORM LIMITATION: agents lack shell execution so make build fails",
		"kind":    "learning",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Already in memory") || !strings.Contains(out, "No agent has shell tools") {
		t.Errorf("remember output should surface the existing text, got %q", out)
	}
	for _, c := range f.calls {
		if c == "mempalace_add_drawer" {
			t.Error("duplicate must not be stored")
		}
	}
}

func TestRememberStoresNonDuplicate(t *testing.T) {
	f := &fakeCall{responses: map[string]string{
		"mempalace_check_duplicate": `{"is_duplicate": false, "matches": []}`,
		"mempalace_add_drawer":      `{"drawer_id": "d1"}`,
	}}
	out, err := execTool(t, newTestProxy(f), "remember", map[string]any{
		"content": "build needs CGO_ENABLED=0, else it fails with linker errors",
		"kind":    "learning",
	})
	if err != nil || !strings.Contains(out, "d1") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	// Content is stored raw — no closet/kind prefix. The task association
	// rides on source_file (already asserted below); the prefix was dropped
	// because it measurably degrades dedup separation (see
	// rememberDupThreshold's doc comment) without which mempalace_search
	// can't recover.
	stored, _ := f.args["mempalace_add_drawer"]["content"].(string)
	if stored != "build needs CGO_ENABLED=0, else it fails with linker errors" {
		t.Errorf("stored content should be raw (no prefix), got %q", stored)
	}
	if src, _ := f.args["mempalace_add_drawer"]["source_file"].(string); src != "tasks/dec-1/run-3" {
		t.Errorf("source_file = %q, want tasks/dec-1/run-3 (task association)", src)
	}
}

func TestWriteDiaryRemovedFromCatalog(t *testing.T) {
	for _, spec := range mpCatalog {
		if spec.name == "write_diary" {
			t.Error("write_diary must not be in the agent tool catalog")
		}
	}
}
