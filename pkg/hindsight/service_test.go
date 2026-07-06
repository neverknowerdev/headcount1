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

// recordingStub captures every request body so tests can assert on exactly
// what the Service sent to Hindsight, without needing a full mock engine.
type recordingStub struct {
	mu         sync.Mutex
	configs    map[string]map[string]interface{} // bank -> last config PATCH
	directives map[string][]map[string]string    // bank -> created directives
	recalls    []map[string]interface{}          // every recall request body, in order
}

func newRecordingStub() *recordingStub {
	return &recordingStub{
		configs:    map[string]map[string]interface{}{},
		directives: map[string][]map[string]string{},
	}
}

func (s *recordingStub) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/default/banks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
		bank, rest, _ := strings.Cut(path, "/")
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case rest == "config" && r.Method == http.MethodPatch:
			var body struct {
				Updates map[string]interface{} `json:"updates"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			s.configs[bank] = body.Updates
			w.WriteHeader(http.StatusOK)
		case rest == "directives" && r.Method == http.MethodGet:
			items := []map[string]string{}
			for _, d := range s.directives[bank] {
				items = append(items, d)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
		case rest == "directives" && r.Method == http.MethodPost:
			var d map[string]string
			json.NewDecoder(r.Body).Decode(&d)
			s.directives[bank] = append(s.directives[bank], d)
			w.WriteHeader(http.StatusOK)
		case rest == "memories" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
		case rest == "memories/recall" && r.Method == http.MethodPost:
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			s.recalls = append(s.recalls, body)
			json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return httptest.NewServer(mux)
}

func TestEnsureBankSetsMissionDispositionAndDirectivesOnce(t *testing.T) {
	stub := newRecordingStub()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	svc := NewService(q, func() *Client { return NewClient(srv.URL) })
	company := db.Company{ID: 7, Name: "Acme"}

	svc.EnsureBank(context.Background(), company)

	bank := BankID(company.ID)
	stub.mu.Lock()
	cfg := stub.configs[bank]
	directives := stub.directives[bank]
	stub.mu.Unlock()

	if cfg == nil {
		t.Fatal("expected a bank config PATCH")
	}
	if !strings.Contains(cfg["reflect_mission"].(string), "Acme") {
		t.Errorf("expected mission to mention the company name, got %v", cfg["reflect_mission"])
	}
	if cfg["disposition_skepticism"] == nil {
		t.Error("expected disposition_skepticism to be set")
	}
	if len(directives) != 2 {
		t.Fatalf("expected 2 directives created, got %d: %v", len(directives), directives)
	}

	// Calling again in the same process must not re-PATCH or re-create
	// directives (EnsureBank is guarded per company per process lifetime).
	stub.mu.Lock()
	stub.configs[bank] = nil // detect a second PATCH by its absence
	stub.mu.Unlock()

	svc.EnsureBank(context.Background(), company)

	stub.mu.Lock()
	secondCfg := stub.configs[bank]
	secondDirectiveCount := len(stub.directives[bank])
	stub.mu.Unlock()
	if secondCfg != nil {
		t.Error("expected no second config PATCH once a bank is ensured")
	}
	if secondDirectiveCount != 2 {
		t.Errorf("expected directive count to stay at 2, got %d", secondDirectiveCount)
	}
}

func TestEnsureBankIsIdempotentAcrossProcessRestarts(t *testing.T) {
	stub := newRecordingStub()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	company := db.Company{ID: 3, Name: "Acme"}

	// First "process": directives get created.
	svc1 := NewService(q, func() *Client { return NewClient(srv.URL) })
	svc1.EnsureBank(context.Background(), company)

	// Second "process" (fresh Service, same backend): directives already
	// exist server-side, so no duplicates should be created.
	svc2 := NewService(q, func() *Client { return NewClient(srv.URL) })
	svc2.EnsureBank(context.Background(), company)

	bank := BankID(company.ID)
	stub.mu.Lock()
	count := len(stub.directives[bank])
	stub.mu.Unlock()
	if count != 2 {
		t.Errorf("expected directives to stay deduplicated across restarts, got %d", count)
	}
}

func TestRecallRequestsObservationsAndPrefersThem(t *testing.T) {
	stub := newRecordingStub()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	svc := NewService(q, func() *Client { return NewClient(srv.URL) })

	if _, err := svc.Recall(context.Background(), 1, nil, "", "what happened", 0); err != nil {
		t.Fatalf("recall: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.recalls) != 1 {
		t.Fatalf("expected exactly one recall request, got %d", len(stub.recalls))
	}
	body := stub.recalls[0]
	types, _ := body["types"].([]interface{})
	if len(types) != 3 {
		t.Errorf("expected 3 recall types (world, experience, observation), got %v", body["types"])
	}
	if body["prefer_observations"] != true {
		t.Errorf("expected prefer_observations=true, got %v", body["prefer_observations"])
	}
	if got := body["max_tokens"].(float64); got != float64(defaultRecallMaxTokens) {
		t.Errorf("expected default max_tokens %d, got %v", defaultRecallMaxTokens, got)
	}
}

func TestFormatResultsFlagsObservations(t *testing.T) {
	out := FormatResults([]RecallResult{
		{Text: "raw fact", Type: "world"},
		{Text: "synthesized knowledge", Type: "observation"},
	})
	if !strings.Contains(out, "[insight] synthesized knowledge") {
		t.Errorf("expected observation to be flagged as [insight], got:\n%s", out)
	}
	if strings.Contains(out, "[insight] raw fact") {
		t.Errorf("did not expect a raw world fact to be flagged as [insight], got:\n%s", out)
	}
}
