package hindsight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestTenantIDAndBankID(t *testing.T) {
	if got := TenantID(7); got != "team-7" {
		t.Errorf("TenantID(7) = %q, want team-7", got)
	}
	if got := BankID(3); got != "company-3" {
		t.Errorf("BankID(3) = %q, want company-3", got)
	}
}

func TestCompanyIDFromBankID(t *testing.T) {
	cases := []struct {
		in   string
		want int32
		ok   bool
	}{
		{"company-3", 3, true},
		{"company-0", 0, true},
		{"company-", 0, false},
		{"company-abc", 0, false},
		{"team-3", 0, false},
		{"foreign-x", 0, false},
		{"", 0, false},
		// Non-canonical spellings must be rejected: each of these parses to 7
		// with a naive Atoi, which would authorize company 7 while the handler
		// addressed a different bank id upstream.
		{"company-+7", 0, false},
		{"company-007", 0, false},
		{"company-4294967303", 0, false}, // int32 truncation -> 7
		{"company--0", 0, false},
		{"company- 7", 0, false},
		{"company-7 ", 0, false},
		{"company-9223372036854775808", 0, false}, // int64 overflow
	}
	for _, c := range cases {
		got, ok := CompanyIDFromBankID(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("CompanyIDFromBankID(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The request path must ALWAYS use hindsight-api's literal tenant segment.
// Verified against a real hindsight-api 0.6.1: its OpenAPI spec mounts all 49
// routes under /v1/default/ (only {bank_id} is a path parameter) and any other
// value 404s — with or without a TenantExtension, since its multi-tenancy is
// credential-based. WithTenant records the team identity but must not alter
// the URL; if it ever does, every memory call breaks against a real backend.
func TestWithTenantDoesNotAlterRequestPath(t *testing.T) {
	c := NewClient("http://example.test")
	const want = "/v1/default/banks/company-3/memories"
	if got := c.bankPath("company-3", "/memories"); got != want {
		t.Errorf("base bankPath = %q, want %q", got, want)
	}
	tc := c.WithTenant(TenantID(7))
	if got := tc.bankPath("company-3", "/memories"); got != want {
		t.Errorf("tenant-scoped bankPath = %q, want %q (hindsight hardcodes the segment)", got, want)
	}
	// The team identity is still carried, for logging / future credential use.
	if tc.Tenant() != "team-7" {
		t.Errorf("Tenant() = %q, want team-7", tc.Tenant())
	}
	// WithTenant must not mutate the original client.
	if c.Tenant() != "default" {
		t.Errorf("WithTenant mutated the base client to %q", c.Tenant())
	}
	if c.WithTenant("").Tenant() != "default" {
		t.Error("empty tenant should fall back to default")
	}
}

// TestClientSendsLiteralTenantInPath asserts the real URL on the wire, so a
// regression that reintroduces path-based tenancy is caught here.
func TestClientSendsLiteralTenantInPath(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tc := NewClient(srv.URL).WithTenant("team-42")
	_ = tc.CreateBank(context.Background(), "company-9")

	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(gotPath, "/v1/default/banks/company-9") {
		t.Errorf("request path = %q, want /v1/default/banks/company-9...", gotPath)
	}
}
