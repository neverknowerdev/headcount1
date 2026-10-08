package runtokens

import (
	"strings"
	"testing"
)

func TestIssueValidateRevoke(t *testing.T) {
	r := NewRegistry()

	token := r.Issue(7)
	if !strings.HasPrefix(token, "rt_") {
		t.Fatalf("unexpected token format: %q", token)
	}
	if runID, ok := r.Validate(token); !ok || runID != 7 {
		t.Fatalf("token must validate to its run, got %d, %v", runID, ok)
	}
	if _, ok := r.Validate("rt_forged"); ok {
		t.Fatal("forged token must not validate")
	}
	if _, ok := r.Validate(""); ok {
		t.Fatal("empty token must not validate")
	}

	r.Revoke(7)
	if _, ok := r.Validate(token); ok {
		t.Fatal("revoked token must not validate")
	}
}

func TestReissueInvalidatesPreviousToken(t *testing.T) {
	r := NewRegistry()
	first := r.Issue(3)
	second := r.Issue(3)
	if _, ok := r.Validate(first); ok {
		t.Fatal("re-issuing must invalidate the prior token")
	}
	if runID, ok := r.Validate(second); !ok || runID != 3 {
		t.Fatal("fresh token must validate")
	}
}

func TestTokensAreDistinctAcrossRuns(t *testing.T) {
	r := NewRegistry()
	a, b := r.Issue(1), r.Issue(2)
	if a == b {
		t.Fatal("distinct runs must get distinct tokens")
	}
	if runID, _ := r.Validate(a); runID != 1 {
		t.Fatal("token a must map to run 1")
	}
	if runID, _ := r.Validate(b); runID != 2 {
		t.Fatal("token b must map to run 2")
	}
}

func TestCompanyTokensValidateAndRevokeIndividually(t *testing.T) {
	r := NewRegistry()
	first, revokeFirst := r.IssueCompany(4)
	second, revokeSecond := r.IssueCompany(4)
	if !strings.HasPrefix(first, "ct_") || first == second {
		t.Fatalf("company tokens must be distinct ct_ tokens, got %q and %q", first, second)
	}
	for _, token := range []string{first, second} {
		if companyID, ok := r.ValidateCompany(token); !ok || companyID != 4 {
			t.Fatalf("company token must validate to its company, got %d, %v", companyID, ok)
		}
	}

	revokeFirst()
	if _, ok := r.ValidateCompany(first); ok {
		t.Fatal("revoked company token must not validate")
	}
	if _, ok := r.ValidateCompany(second); !ok {
		t.Fatal("revoking one company token must leave the company's other tokens valid")
	}
	revokeSecond()
	if _, ok := r.ValidateCompany(second); ok {
		t.Fatal("revoked company token must not validate")
	}
}

func TestRunAndCompanyTokensAreNotInterchangeable(t *testing.T) {
	r := NewRegistry()
	runToken := r.Issue(9)
	companyToken, revoke := r.IssueCompany(9)
	defer revoke()

	if _, ok := r.ValidateCompany(runToken); ok {
		t.Fatal("a run token must not validate as a company token")
	}
	if _, ok := r.Validate(companyToken); ok {
		t.Fatal("a company token must not validate as a run token")
	}
}
