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

func TestWithTenantRoutesBankPath(t *testing.T) {
	c := NewClient("http://example.test")
	if c.tenantSeg() != "default" {
		t.Errorf("new client tenant = %q, want default", c.tenantSeg())
	}
	if got := c.bankPath("company-3", "/memories"); got != "/v1/default/banks/company-3/memories" {
		t.Errorf("default bankPath = %q", got)
	}
	tc := c.WithTenant(TenantID(7))
	if got := tc.bankPath("company-3", "/memories"); got != "/v1/team-7/banks/company-3/memories" {
		t.Errorf("tenant bankPath = %q, want /v1/team-7/banks/company-3/memories", got)
	}
	// WithTenant must not mutate the original client.
	if c.tenantSeg() != "default" {
		t.Errorf("WithTenant mutated the base client tenant to %q", c.tenantSeg())
	}
	if c.WithTenant("").tenantSeg() != "default" {
		t.Error("empty tenant should fall back to default")
	}
}

// TestClientSendsTenantInPath asserts the real request URL a tenant-scoped
// client sends carries the tenant segment (end-to-end through the http path).
func TestClientSendsTenantInPath(t *testing.T) {
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
	if !strings.HasPrefix(gotPath, "/v1/team-42/banks/company-9") {
		t.Errorf("request path = %q, want /v1/team-42/banks/company-9...", gotPath)
	}
}
