package hindsight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agent-orchestrator/db"
)

// docStub records retained items and deleted documents so tests can assert
// the full doc-sync lifecycle (add, update, remove, skip).
type docStub struct {
	mu       sync.Mutex
	retained []MemoryItem // every item ever retained, in order
	deleted  []string     // document ids deleted
}

func (s *docStub) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		// /v1/<tenant>/banks/<bank>/<rest...> — tenant-agnostic.
		afterV1 := strings.TrimPrefix(r.URL.Path, "/v1/")
		_, afterTenant, _ := strings.Cut(afterV1, "/")
		path := strings.TrimPrefix(afterTenant, "banks/")
		_, rest, _ := strings.Cut(path, "/")
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case rest == "config" && r.Method == http.MethodPatch,
			rest == "directives" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case rest == "directives" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{"items": []interface{}{}})
		case rest == "memories" && r.Method == http.MethodPost:
			var req retainRequest
			json.NewDecoder(r.Body).Decode(&req)
			s.retained = append(s.retained, req.Items...)
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
		case strings.HasPrefix(rest, "documents/") && r.Method == http.MethodDelete:
			// r.URL.Path is already percent-decoded by net/http.
			s.deleted = append(s.deleted, strings.TrimPrefix(rest, "documents/"))
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return httptest.NewServer(mux)
}

func (s *docStub) snapshot() ([]MemoryItem, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MemoryItem(nil), s.retained...), append([]string(nil), s.deleted...)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newSyncFixture(t *testing.T) (*Service, *docStub, db.Company, db.Project, string) {
	t.Helper()
	stub := &docStub{}
	srv := stub.server()
	t.Cleanup(srv.Close)

	q := testDB(t)
	svc := NewService(q, func() *Client { return NewClient(srv.URL) })
	company := db.Company{ID: 1, Name: "Acme", TeamID: &testTeamID}
	project := db.Project{ID: 5, Name: "Widgets"}
	repo := t.TempDir()
	return svc, stub, company, project, repo
}

func TestSyncProjectDocsLifecycle(t *testing.T) {
	svc, stub, company, project, repo := newSyncFixture(t)
	ctx := context.Background()

	writeFile(t, repo, "README.md", "# Widgets\nDocs v1")
	writeFile(t, repo, "docs/design.md", "design doc")
	writeFile(t, repo, "main.go", "package main")                // not a doc: ignored
	writeFile(t, repo, "node_modules/dep/README.md", "vendored") // skipped dir

	added, updated, removed, err := svc.SyncProjectDocs(ctx, company, project, repo)
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if added != 2 || updated != 0 || removed != 0 {
		t.Fatalf("initial sync: expected 2/0/0, got %d/%d/%d", added, updated, removed)
	}
	retained, _ := stub.snapshot()
	if len(retained) != 2 {
		t.Fatalf("expected 2 retained items, got %d", len(retained))
	}
	byDoc := map[string]MemoryItem{}
	for _, it := range retained {
		byDoc[it.DocumentID] = it
	}
	readme, ok := byDoc["doc:5/README.md"]
	if !ok {
		t.Fatalf("expected doc:5/README.md, got %v", byDoc)
	}
	if readme.Timestamp != "unset" {
		t.Errorf("doc memories must be timeless, got timestamp %q", readme.Timestamp)
	}
	wantTags := []string{"project:widgets", "source:docs"} // tag is the project name slug
	for i, tag := range wantTags {
		if readme.Tags[i] != tag {
			t.Errorf("expected tags %v, got %v", wantTags, readme.Tags)
		}
	}
	if len(readme.ObservationScopes) != 1 || readme.ObservationScopes[0][0] != "project:widgets" {
		t.Errorf("expected project-scoped observation, got %v", readme.ObservationScopes)
	}

	// Second sync with no changes: everything skipped by hash.
	added, updated, removed, err = svc.SyncProjectDocs(ctx, company, project, repo)
	if err != nil {
		t.Fatalf("no-op sync: %v", err)
	}
	if added != 0 || updated != 0 || removed != 0 {
		t.Fatalf("no-op sync: expected 0/0/0, got %d/%d/%d", added, updated, removed)
	}
	if retained, _ := stub.snapshot(); len(retained) != 2 {
		t.Fatalf("no-op sync must not re-retain, got %d items", len(retained))
	}

	// Change one file, delete the other: one update + one removal.
	writeFile(t, repo, "README.md", "# Widgets\nDocs v2")
	if err := os.Remove(filepath.Join(repo, "docs", "design.md")); err != nil {
		t.Fatal(err)
	}
	added, updated, removed, err = svc.SyncProjectDocs(ctx, company, project, repo)
	if err != nil {
		t.Fatalf("update sync: %v", err)
	}
	if added != 0 || updated != 1 || removed != 1 {
		t.Fatalf("update sync: expected 0/1/1, got %d/%d/%d", added, updated, removed)
	}
	_, deleted := stub.snapshot()
	if len(deleted) != 1 || deleted[0] != "doc:5/docs/design.md" {
		t.Errorf("expected doc:5/docs/design.md deleted, got %v", deleted)
	}
}

func TestSyncProjectDocsSkipsOversizedFiles(t *testing.T) {
	svc, stub, company, project, repo := newSyncFixture(t)

	writeFile(t, repo, "small.md", "fine")
	writeFile(t, repo, "huge.md", strings.Repeat("x", maxDocFileBytes+1))

	added, _, _, err := svc.SyncProjectDocs(context.Background(), company, project, repo)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if added != 1 {
		t.Fatalf("expected only the small file retained, got added=%d", added)
	}
	retained, _ := stub.snapshot()
	if len(retained) != 1 || retained[0].DocumentID != "doc:5/small.md" {
		t.Errorf("expected only doc:5/small.md, got %v", retained)
	}
}

func TestRetainRunOutcomePayload(t *testing.T) {
	svc, stub, company, _, _ := newSyncFixture(t)

	projectID := int32(7)
	rootRunID := int32(100)
	task := db.Task{ID: 9, CompanyID: company.ID, ProjectID: &projectID,
		Project: &db.Project{ID: projectID, Name: "GM Coin"},
		RefKey:  "DEC-50", Title: "Implement ICP backend", Status: "done"}
	run := db.Run{ID: 42, TaskID: task.ID, Name: "run-42",
		AgentConfigName: "CTO", RootRunID: &rootRunID,
		ResultDescription: "Implemented and tested"}

	if err := svc.RetainRunOutcome(context.Background(), company, task, run, "completed", ""); err != nil {
		t.Fatalf("RetainRunOutcome: %v", err)
	}
	retained, _ := stub.snapshot()
	if len(retained) != 1 {
		t.Fatalf("expected 1 retained item, got %d", len(retained))
	}
	item := retained[0]
	if item.DocumentID != "run-42" {
		t.Errorf("expected document id run-42, got %q", item.DocumentID)
	}
	wantTags := map[string]bool{"agent:cto": true, "session:100": true, "task:dec-50": true, "project:gm-coin": true}
	if len(item.Tags) != len(wantTags) {
		t.Fatalf("expected tags %v, got %v", wantTags, item.Tags)
	}
	for _, tag := range item.Tags {
		if !wantTags[tag] {
			t.Errorf("unexpected tag %q (want %v)", tag, wantTags)
		}
	}
	if len(item.ObservationScopes) != 2 {
		t.Errorf("expected agent + project observation scopes, got %v", item.ObservationScopes)
	}
	if item.Timestamp == "" || item.Timestamp == "unset" {
		t.Errorf("run outcomes must carry a real timestamp, got %q", item.Timestamp)
	}
	if !strings.Contains(item.Content, "DEC-50") || !strings.Contains(item.Content, "completed") {
		t.Errorf("expected content to mention task and status, got %q", item.Content)
	}
}

func TestResetEnsuredReappliesBankConfig(t *testing.T) {
	stub := newRecordingStub()
	srv := stub.server()
	defer srv.Close()

	svc := NewService(testDB(t), func() *Client { return NewClient(srv.URL) })
	company := db.Company{ID: 1, Name: "Acme", TeamID: &testTeamID}

	svc.EnsureBank(context.Background(), company)
	bank := BankID(company.ID)
	stub.mu.Lock()
	stub.configs[bank] = nil
	stub.mu.Unlock()

	// Without a reset, EnsureBank is a no-op...
	svc.EnsureBank(context.Background(), company)
	stub.mu.Lock()
	patched := stub.configs[bank] != nil
	stub.mu.Unlock()
	if patched {
		t.Fatal("expected no re-PATCH before reset")
	}

	// ...after ResetEnsured it re-applies the config (e2e DB wipe reuses IDs).
	svc.ResetEnsured()
	svc.EnsureBank(context.Background(), company)
	stub.mu.Lock()
	patched = stub.configs[bank] != nil
	stub.mu.Unlock()
	if !patched {
		t.Fatal("expected bank config to be re-applied after ResetEnsured")
	}
}

func TestFirstNIsRuneSafe(t *testing.T) {
	s := "héllo wörld" // multi-byte runes
	for n := 0; n <= len(s)+1; n++ {
		got := firstN(s, n)
		if !strings.HasSuffix(got, "…") && got != s {
			t.Errorf("firstN(%q, %d) = %q: neither full string nor truncated", s, n, got)
		}
		trimmed := strings.TrimSuffix(got, "…")
		for _, r := range trimmed {
			if r == '�' {
				t.Errorf("firstN(%q, %d) = %q contains a broken rune", s, n, got)
			}
		}
	}
	if firstN("abc", 10) != "abc" {
		t.Error("short strings must pass through unchanged")
	}
}
