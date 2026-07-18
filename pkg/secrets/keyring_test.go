package secrets

import (
	"sync"
	"testing"
	"time"
)

func TestKeyringPutGetEvict(t *testing.T) {
	k := NewKeyring()
	var dek [32]byte
	dek[0], dek[31] = 1, 9

	if _, ok := k.Get(7); ok {
		t.Fatal("empty keyring must not return a DEK")
	}

	k.Put(7, dek, time.Minute)
	got, ok := k.Get(7)
	if !ok || got != dek {
		t.Fatalf("Get after Put = %v, %v; want the stored DEK", got, ok)
	}
	if k.Len() != 1 {
		t.Fatalf("Len = %d, want 1", k.Len())
	}

	k.Evict(7)
	if _, ok := k.Get(7); ok {
		t.Fatal("Get after Evict must miss")
	}
}

func TestKeyringTTLExpiry(t *testing.T) {
	k := NewKeyring()
	var dek [32]byte
	dek[0] = 42

	// Already-expired TTL: the entry must be treated as absent and reaped.
	k.Put(3, dek, -time.Second)
	if _, ok := k.Get(3); ok {
		t.Fatal("expired entry must not be returned")
	}
	if k.Len() != 0 {
		t.Fatalf("expired entry must be evicted lazily; Len = %d", k.Len())
	}
}

func TestKeyringSnapshotRestore(t *testing.T) {
	k := NewKeyring()
	var a, b, expired [32]byte
	a[0], b[0], expired[0] = 1, 2, 3
	k.Put(1, a, time.Minute)
	k.Put(2, b, time.Minute)
	k.Put(9, expired, -time.Second) // must not survive the snapshot

	snap := k.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot size = %d, want 2 (expired excluded)", len(snap))
	}

	restored := NewKeyring()
	restored.Restore(snap, time.Minute)
	if got, ok := restored.Get(1); !ok || got != a {
		t.Fatal("restore lost user 1")
	}
	if got, ok := restored.Get(2); !ok || got != b {
		t.Fatal("restore lost user 2")
	}
	if _, ok := restored.Get(9); ok {
		t.Fatal("expired user 9 must not be restored")
	}
}

func TestKeyringConcurrent(t *testing.T) {
	k := NewKeyring()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			var dek [32]byte
			dek[0] = byte(id)
			k.Put(id, dek, time.Minute)
			k.Get(id)
			k.Snapshot()
			if id%2 == 0 {
				k.Evict(id)
			}
		}(int32(i))
	}
	wg.Wait()
}
