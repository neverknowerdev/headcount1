package secrets

import (
	"bytes"
	"testing"
	"time"
)

func TestWrapUnwrapDEKForPRFRoundTrip(t *testing.T) {
	dek, err := NewUserDEK()
	if err != nil {
		t.Fatal(err)
	}
	prf := bytes.Repeat([]byte{0xAB}, 32)

	wrapped, err := WrapDEKForPRF(dek, prf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapDEKWithPRF(wrapped, prf)
	if err != nil {
		t.Fatal(err)
	}
	if got != dek {
		t.Fatal("unwrapped DEK differs from original")
	}
}

func TestUnwrapWrongPRFFails(t *testing.T) {
	dek, _ := NewUserDEK()
	wrapped, _ := WrapDEKForPRF(dek, bytes.Repeat([]byte{1}, 32))
	if _, err := UnwrapDEKWithPRF(wrapped, bytes.Repeat([]byte{2}, 32)); err == nil {
		t.Fatal("a different PRF output must not open the wrap")
	}
}

func TestDistinctPRFProducesDistinctWraps(t *testing.T) {
	dek, _ := NewUserDEK()
	w1, _ := WrapDEKForPRF(dek, bytes.Repeat([]byte{1}, 32))
	w2, _ := WrapDEKForPRF(dek, bytes.Repeat([]byte{2}, 32))
	if w1 == w2 {
		t.Fatal("different PRF outputs (i.e. different passkeys) must yield different wraps")
	}
	// Both still open to the same DEK with their own PRF.
	g1, _ := UnwrapDEKWithPRF(w1, bytes.Repeat([]byte{1}, 32))
	g2, _ := UnwrapDEKWithPRF(w2, bytes.Repeat([]byte{2}, 32))
	if g1 != dek || g2 != dek {
		t.Fatal("each per-credential wrap must recover the same DEK")
	}
}

func TestPRFDerivationDeterministic(t *testing.T) {
	prf := bytes.Repeat([]byte{7}, 32)
	k1, err := deriveKeyFromPRF(prf)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := deriveKeyFromPRF(prf)
	if k1 != k2 {
		t.Fatal("HKDF over the same PRF output must be deterministic")
	}
}

func TestTamperedWrapFails(t *testing.T) {
	dek, _ := NewUserDEK()
	prf := bytes.Repeat([]byte{9}, 32)
	wrapped, _ := WrapDEKForPRF(dek, prf)
	// Flip a byte in the base64 body.
	b := []byte(wrapped)
	b[len(b)-2] ^= 0xFF
	if _, err := UnwrapDEKWithPRF(string(b), prf); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}

func TestStoreUnlockLockIsUnlocked(t *testing.T) {
	s := NewStore(&fileKeySource{path: t.TempDir() + "/master.key"}, t.TempDir()+"/keystore.json")
	dek, _ := NewUserDEK()

	if s.IsUnlocked(5) {
		t.Fatal("user should start locked")
	}
	s.UnlockUser(5, dek, time.Minute)
	if !s.IsUnlocked(5) {
		t.Fatal("user should be unlocked after UnlockUser")
	}
	got, ok := s.userDEKFromKeyring(5)
	if !ok || got != dek {
		t.Fatal("keyring should return the unlocked DEK")
	}
	s.LockUser(5)
	if s.IsUnlocked(5) {
		t.Fatal("user should be locked after LockUser")
	}
}
