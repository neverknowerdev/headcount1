package hindsight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// stubHindsight is a minimal in-memory stand-in for the parts of the
// Hindsight HTTP API MigrateLegacyBanks depends on: bank listing, document
// export/import (opaque byte payloads, echoed back), and bank deletion.
type stubHindsight struct {
	mu      sync.Mutex
	exports map[string][]byte // bank -> exported bytes
	deleted map[string]bool
	banks   []string
}

func newStubHindsight() *stubHindsight {
	return &stubHindsight{exports: map[string][]byte{}, deleted: map[string]bool{}}
}

func (s *stubHindsight) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/default/banks", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var banks []BankInfo
		for _, b := range s.banks {
			if !s.deleted[b] {
				banks = append(banks, BankInfo{BankID: b})
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"banks": banks})
	})
	mux.HandleFunc("/v1/default/banks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
		switch {
		case strings.HasSuffix(path, "/document-transfer") && r.Method == http.MethodGet:
			bank := strings.TrimSuffix(path, "/document-transfer")
			s.mu.Lock()
			data := s.exports[bank]
			s.mu.Unlock()
			w.Write(data)
		case strings.HasSuffix(path, "/document-transfer") && r.Method == http.MethodPost:
			bank := strings.TrimSuffix(path, "/document-transfer")
			r.ParseMultipartForm(10 << 20)
			file, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			buf := make([]byte, 1<<20)
			n, _ := file.Read(buf)
			s.mu.Lock()
			s.exports[bank] = buf[:n]
			if !contains(s.banks, bank) {
				s.banks = append(s.banks, bank)
			}
			s.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"operation_id": "op-1"})
		case r.Method == http.MethodDelete && !strings.Contains(path, "/"):
			bank := path
			s.mu.Lock()
			s.deleted[bank] = true
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return httptest.NewServer(mux)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func testDB(t *testing.T) *db.Queries {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&db.Company{}, &db.Project{}, &db.HindsightDocument{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db.New(gdb)
}

func TestMigrateLegacyBanks(t *testing.T) {
	stub := newStubHindsight()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	company, err := q.CreateCompany(context.Background(), "Acme")
	if err != nil {
		t.Fatalf("create company: %v", err)
	}
	project, err := q.CreateProject(context.Background(), db.Project{CompanyID: company.ID, Name: "Widgets", ID: 10})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if project.ID != 10 {
		t.Fatalf("expected seeded project id 10, got %d (sqlite may not honor explicit PK on insert)", project.ID)
	}
	if err := q.UpsertHindsightDoc(context.Background(), db.HindsightDocument{
		ProjectID: project.ID, Path: "README.md", DocumentID: "doc:README.md", SHA256: "abc",
	}); err != nil {
		t.Fatalf("seed hindsight doc: %v", err)
	}

	// Seed legacy banks: the company's run-experience bank (with some
	// exported bytes to carry over) and the project's docs bank (expected to
	// be dropped and re-derived from disk, not carried).
	stub.mu.Lock()
	stub.exports["runs-"+strconv.Itoa(int(company.ID))] = []byte(`{"documents":[{"id":"run-5"}]}`)
	stub.banks = []string{"runs-" + strconv.Itoa(int(company.ID)), "proj-10"}
	stub.mu.Unlock()

	svc := NewService(q, func() *Client { return NewClient(srv.URL) })

	tmpDir := t.TempDir()
	if err := svc.MigrateLegacyBanks(context.Background(), tmpDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Target bank now holds the carried-over run-experience export.
	target := BankID(company.ID)
	stub.mu.Lock()
	got := string(stub.exports[target])
	stub.mu.Unlock()
	if !strings.Contains(got, "run-5") {
		t.Errorf("expected run experience carried into %s, got %q", target, got)
	}

	// Legacy banks deleted.
	stub.mu.Lock()
	runsDeleted := stub.deleted["runs-"+strconv.Itoa(int(company.ID))]
	projDeleted := stub.deleted["proj-10"]
	stub.mu.Unlock()
	if !runsDeleted {
		t.Error("expected legacy runs bank to be deleted")
	}
	if !projDeleted {
		t.Error("expected legacy proj bank to be deleted")
	}

	// Doc tracking wiped so the next sync re-ingests from disk into the new bank.
	docs, err := q.ListHindsightDocsByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("list docs: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected doc tracking cleared, got %d rows", len(docs))
	}

	// Marker written; re-running is a no-op (no panics, no further mutation).
	marker := migrationMarker(tmpDir)
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("expected migration marker at %s: %v", marker, statErr)
	}
	if err := svc.MigrateLegacyBanks(context.Background(), tmpDir); err != nil {
		t.Fatalf("second migrate call should no-op, got error: %v", err)
	}
}

func TestMigrateLegacyBanksNoLegacyBanksIsNoop(t *testing.T) {
	stub := newStubHindsight()
	srv := stub.server()
	defer srv.Close()

	q := testDB(t)
	svc := NewService(q, func() *Client { return NewClient(srv.URL) })

	tmpDir := t.TempDir()
	if err := svc.MigrateLegacyBanks(context.Background(), tmpDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(migrationMarker(tmpDir)); err != nil {
		t.Errorf("expected marker written even with nothing to migrate: %v", err)
	}
}
