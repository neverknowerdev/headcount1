package secrets

import (
	"sync"
	"time"
)

// Keyring holds unlocked per-user DEKs in memory only. A DEK enters the
// keyring when the user authenticates (passkey PRF unlock) and is evicted on
// logout or when its TTL lapses. In the steady state nothing here is ever
// written to disk — that is what makes the encryption zero-knowledge: with no
// user logged in, no key on the box can open their secrets. The graceful-exit
// path (Store.SealKeyring) is the only time a snapshot leaves memory, and then
// only wrapped by the boot key so an unexpected crash leaves nothing behind.
type Keyring struct {
	mu   sync.RWMutex
	deks map[int32]keyringEntry
}

type keyringEntry struct {
	dek       [32]byte
	expiresAt time.Time
}

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring {
	return &Keyring{deks: make(map[int32]keyringEntry)}
}

// Put unlocks a user: their DEK becomes available for the given ttl.
func (k *Keyring) Put(userID int32, dek [32]byte, ttl time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deks[userID] = keyringEntry{dek: dek, expiresAt: time.Now().Add(ttl)}
}

// Get returns the user's DEK if present and unexpired. Expired entries are
// evicted lazily. This is a pure in-memory read — safe to call from inside a
// GORM row Scan while the (possibly single, SQLite) DB connection is held.
func (k *Keyring) Get(userID int32) ([32]byte, bool) {
	k.mu.RLock()
	e, ok := k.deks[userID]
	k.mu.RUnlock()
	if !ok {
		return [32]byte{}, false
	}
	if time.Now().After(e.expiresAt) {
		k.Evict(userID)
		return [32]byte{}, false
	}
	return e.dek, true
}

// Evict locks a user out: their DEK is dropped and their secrets become
// undecryptable until the next unlock. Called on logout.
func (k *Keyring) Evict(userID int32) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.deks, userID)
}

// EvictExpired proactively drops every entry whose TTL has lapsed. Get already
// evicts lazily on access, but a user who stops making requests would otherwise
// keep their DEK resident until the next touch; a periodic sweep bounds how long
// a dead session's key lingers in memory. Returns the number evicted.
func (k *Keyring) EvictExpired() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	n := 0
	for id, e := range k.deks {
		if now.After(e.expiresAt) {
			delete(k.deks, id)
			n++
		}
	}
	return n
}

// Len reports how many users are currently unlocked (unexpired entries may be
// counted until their lazy eviction; used for diagnostics only).
func (k *Keyring) Len() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.deks)
}

// Snapshot returns a copy of all live (unexpired) DEKs, for the graceful-exit
// seal. Expired entries are skipped.
func (k *Keyring) Snapshot() map[int32][32]byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	now := time.Now()
	out := make(map[int32][32]byte, len(k.deks))
	for id, e := range k.deks {
		if now.After(e.expiresAt) {
			continue
		}
		out[id] = e.dek
	}
	return out
}

// Restore re-populates the keyring from a snapshot (planned-restart re-warm),
// giving each entry a fresh TTL.
func (k *Keyring) Restore(deks map[int32][32]byte, ttl time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	exp := time.Now().Add(ttl)
	for id, dek := range deks {
		k.deks[id] = keyringEntry{dek: dek, expiresAt: exp}
	}
}
