package hindsight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agent-orchestrator/db"
)

// modelStub is a minimal stand-in for the mental-model endpoints: created
// models start with placeholder content (mirroring Hindsight's async
// creation), and a test can "finish" generation by setting real content.
type modelStub struct {
	mu     sync.Mutex
	models map[string]map[string]interface{} // bank -> modelID -> {name, source_query, tags, content}
}

func newModelStub() *modelStub {
	return &modelStub{models: map[string]map[string]interface{}{}}
}

func (s *modelStub) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/default/banks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
		bank, rest, _ := strings.Cut(path, "/")
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.models[bank] == nil {
			s.models[bank] = map[string]interface{}{}
		}
		switch {
		case rest == "mental-models" && r.Method == http.MethodPost:
			var req struct {
				ID          string   `json:"id"`
				Name        string   `json:"name"`
				SourceQuery string   `json:"source_query"`
				Tags        []string `json:"tags"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			s.models[bank][req.ID] = map[string]interface{}{
				"id": req.ID, "name": req.Name, "source_query": req.SourceQuery,
				"content": "Generating content...",
			}
			json.NewEncoder(w).Encode(map[string]string{"mental_model_id": req.ID, "operation_id": "op-1"})
		case strings.HasPrefix(rest, "mental-models/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(rest, "mental-models/")
			m, ok := s.models[bank][id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(m)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return httptest.NewServer(mux)
}

func (s *modelStub) setContent(bank, id, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.models[bank][id].(map[string]interface{})
	m["content"] = content
}

func TestEnsureProjectStateModelCreatesOnceAndFetchesContent(t *testing.T) {
	stub := newModelStub()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	svc := NewService(q, func() *Client { return NewClient(srv.URL) })
	company := db.Company{ID: 1, Name: "Acme"}
	project := db.Project{ID: 42, Name: "Widgets"}

	svc.EnsureProjectStateModel(context.Background(), company, project)

	bank := BankID(company.ID)
	modelID := ProjectStateModelID(project.ID)
	stub.mu.Lock()
	_, exists := stub.models[bank][modelID]
	stub.mu.Unlock()
	if !exists {
		t.Fatal("expected project-state model to be created")
	}

	// Placeholder content is not yet usable.
	if _, ok := svc.FetchModelContent(context.Background(), company.ID, modelID); ok {
		t.Error("expected placeholder content to be reported as not-ready")
	}

	// Once generation finishes, content is returned.
	stub.setContent(bank, modelID, "ICP backend is implemented as of DEC-50.")
	content, ok := svc.FetchModelContent(context.Background(), company.ID, modelID)
	if !ok {
		t.Fatal("expected content once generation finished")
	}
	if !strings.Contains(content, "ICP backend") {
		t.Errorf("unexpected content: %q", content)
	}

	// Calling Ensure again (same process) must not create a duplicate.
	svc.EnsureProjectStateModel(context.Background(), company, project)
	stub.mu.Lock()
	count := len(stub.models[bank])
	stub.mu.Unlock()
	if count != 1 {
		t.Errorf("expected exactly one model, got %d", count)
	}
}

func TestEnsureAgentPlaybookAndOpenBlockersModelIDs(t *testing.T) {
	stub := newModelStub()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	svc := NewService(q, func() *Client { return NewClient(srv.URL) })
	company := db.Company{ID: 9, Name: "Acme"}

	svc.EnsureAgentPlaybookModel(context.Background(), company, "CTO")
	svc.EnsureOpenBlockersModel(context.Background(), company)

	bank := BankID(company.ID)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if _, ok := stub.models[bank][AgentPlaybookModelID("CTO")]; !ok {
		t.Error("expected agent playbook model to be created with the deterministic id")
	}
	if _, ok := stub.models[bank][OpenBlockersModelID()]; !ok {
		t.Error("expected open-blockers model to be created")
	}
	if AgentPlaybookModelID("CTO") != "agent-playbook-cto" {
		t.Errorf("expected deterministic lowercase id, got %q", AgentPlaybookModelID("CTO"))
	}
}
