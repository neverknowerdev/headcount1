package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"
	"time"
)

// fakeUnwrapper is a self-contained AES-GCM boot key for testing SealKeyring
// without importing pkg/bootkey (which would be a cycle).
type fakeUnwrapper struct{ key [32]byte }

func newFakeUnwrapper() *fakeUnwrapper {
	var k [32]byte
	rand.Read(k[:])
	return &fakeUnwrapper{key: k}
}
func (f *fakeUnwrapper) Name() string { return "fake" }
func (f *fakeUnwrapper) gcm() cipher.AEAD {
	b, _ := aes.NewCipher(f.key[:])
	g, _ := cipher.NewGCM(b)
	return g
}
func (f *fakeUnwrapper) Seal(p []byte) ([]byte, error) {
	g := f.gcm()
	n := make([]byte, g.NonceSize())
	rand.Read(n)
	return g.Seal(n, n, p, nil), nil
}
func (f *fakeUnwrapper) Unseal(c []byte) ([]byte, error) {
	g := f.gcm()
	return g.Open(nil, c[:g.NonceSize()], c[g.NonceSize():], nil)
}

func TestSealUnsealKeyringRoundTrip(t *testing.T) {
	s := NewManager()
	var d1, d2 [32]byte
	d1[0], d2[0] = 1, 2
	s.UnlockUser(1, d1, time.Minute)
	s.UnlockUser(2, d2, time.Minute)

	u := newFakeUnwrapper()
	blob, err := s.SealKeyring(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 {
		t.Fatal("expected a non-empty sealed blob for 2 unlocked users")
	}

	// Restart simulation: fresh store, restore from the sealed blob.
	s2 := NewManager()
	if s2.IsUnlocked(1) {
		t.Fatal("fresh store must start locked")
	}
	if err := s2.UnsealKeyring(u, blob, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, ok := s2.userDEKFromKeyring(1); !ok || got != d1 {
		t.Fatal("user 1 DEK not restored")
	}
	if got, ok := s2.userDEKFromKeyring(2); !ok || got != d2 {
		t.Fatal("user 2 DEK not restored")
	}
}

func TestSealEmptyKeyringIsNoop(t *testing.T) {
	s := NewManager()
	blob, err := s.SealKeyring(newFakeUnwrapper())
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 0 {
		t.Fatal("sealing an empty keyring must produce no blob (nothing to persist)")
	}
}
