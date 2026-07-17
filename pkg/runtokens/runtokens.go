// Package runtokens issues short-lived bearer tokens that authenticate agent
// runs to the in-process LLM gateway. The gateway's machine routes cannot use
// session cookies (the caller is an agent loop, not a browser), and leaving
// them open would let anyone who can reach the port spend tenants' stored
// LLM credentials. Instead the engine mints a token when a run starts and
// revokes it when the run ends; the gateway accepts proxy traffic only from
// live runs (or an authenticated user session — see integration).
//
// Tokens are process-local by design: engine and gateway live in the same
// binary, and a server restart invalidates all tokens together with the runs
// they belonged to (stale runs are failed at startup anyway).
package runtokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"
)

// TokenHeader carries the per-run token on gateway proxy calls.
const TokenHeader = "X-Gateway-Token"

type Registry struct {
	mu     sync.Mutex
	byHash map[string]int32 // sha256(token) → run ID
	byRun  map[int32]string // run ID → sha256(token), for revocation
}

func NewRegistry() *Registry {
	return &Registry{byHash: map[string]int32{}, byRun: map[int32]string{}}
}

// defaultRegistry is shared between the engine (issuer) and the gateway
// (validator), which live in the same process.
var defaultRegistry = NewRegistry()

func Default() *Registry { return defaultRegistry }

// Issue mints a fresh token for a run, replacing any previous one.
func (r *Registry) Issue(runID int32) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the process is in a hopeless state;
		// returning an unusable token fails closed at the gateway.
		return ""
	}
	token := "rt_" + base64.RawURLEncoding.EncodeToString(b)
	h := hashToken(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byRun[runID]; ok {
		delete(r.byHash, old)
	}
	r.byHash[h] = runID
	r.byRun[runID] = h
	return token
}

// Validate resolves a presented token to its run ID.
func (r *Registry) Validate(token string) (int32, bool) {
	if token == "" {
		return 0, false
	}
	h := hashToken(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	runID, ok := r.byHash[h]
	return runID, ok
}

// Revoke invalidates the run's token (called when the run ends).
func (r *Registry) Revoke(runID int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.byRun[runID]; ok {
		delete(r.byHash, h)
		delete(r.byRun, runID)
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
