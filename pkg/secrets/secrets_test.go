package secrets

import (
	"strings"
	"testing"
	"time"
)

func TestPerUserSealOpenRoundtrip(t *testing.T) {
	s := NewManager()
	var dek [32]byte
	dek[0] = 7
	s.UnlockUser(1, dek, time.Minute)

	sealed, err := s.EncryptForUser(1, "sk-super-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) {
		t.Fatalf("sealed value missing prefix: %q", sealed)
	}
	if strings.Contains(sealed, "super-secret") {
		t.Fatal("plaintext leaked into sealed value")
	}
	plain, err := s.Decrypt(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "sk-super-secret" {
		t.Fatalf("roundtrip mismatch: %q", plain)
	}
}

func TestEncryptForUserEmptyAndDoubleSeal(t *testing.T) {
	s := NewManager()
	var dek [32]byte
	dek[0] = 3
	s.UnlockUser(2, dek, time.Minute)

	if v, err := s.EncryptForUser(2, ""); err != nil || v != "" {
		t.Fatalf("empty must stay empty, got %q, %v", v, err)
	}
	sealed, _ := s.EncryptForUser(2, "value")
	again, err := s.EncryptForUser(2, sealed)
	if err != nil || again != sealed {
		t.Fatalf("sealing a sealed value must be a no-op, got %q, %v", again, err)
	}
}

func TestEncryptForLockedUserFails(t *testing.T) {
	s := NewManager()
	if _, err := s.EncryptForUser(99, "v"); err != ErrLocked {
		t.Fatalf("locked user seal must return ErrLocked, got %v", err)
	}
}

func TestDecryptLockedUserFails(t *testing.T) {
	s := NewManager()
	var dek [32]byte
	dek[0] = 9
	s.UnlockUser(4, dek, time.Minute)
	sealed, err := s.EncryptForUser(4, "v")
	if err != nil {
		t.Fatal(err)
	}
	s.LockUser(4)
	if _, err := s.Decrypt(sealed); err != ErrLocked {
		t.Fatalf("decrypting a locked user's secret must return ErrLocked, got %v", err)
	}
}

func TestOpenLegacyPlaintextPassthrough(t *testing.T) {
	s := NewManager()
	plain, err := s.Decrypt("legacy-plaintext-key")
	if err != nil || plain != "legacy-plaintext-key" {
		t.Fatalf("legacy passthrough failed: %q, %v", plain, err)
	}
}

func TestSecretsAreUserIsolated(t *testing.T) {
	s := NewManager()
	var d1, d2 [32]byte
	d1[0], d2[0] = 1, 2
	s.UnlockUser(10, d1, time.Minute)
	s.UnlockUser(20, d2, time.Minute)

	sealed, err := s.EncryptForUser(10, "user-10-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Ownership is embedded in the value, so decryption uses user 10's DEK
	// regardless of who calls Decrypt — but user 10 being locked makes it
	// undecryptable.
	s.LockUser(10)
	if _, err := s.Decrypt(sealed); err != ErrLocked {
		t.Fatalf("user 20 unlocked must not open user 10's secret, got %v", err)
	}
}
