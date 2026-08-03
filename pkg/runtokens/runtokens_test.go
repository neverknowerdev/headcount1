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
