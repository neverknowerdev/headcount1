package secrets

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// memUserKeyStorage is an in-memory UserKeyStorage for tests.
type memUserKeyStorage struct{ recs map[int32]UserKeyRecord }

func (m *memUserKeyStorage) GetUserKey(userID int32) (UserKeyRecord, error) {
	rec, ok := m.recs[userID]
	if !ok {
		return UserKeyRecord{}, fmt.Errorf("no key for user %d", userID)
	}
	return rec, nil
}

func (m *memUserKeyStorage) ListUserKeys() (map[int32]UserKeyRecord, error) {
	recs := make(map[int32]UserKeyRecord, len(m.recs))
	for id, rec := range m.recs {
		recs[id] = rec
	}
	return recs, nil
}

func (m *memUserKeyStorage) SaveUserKey(userID int32, rec UserKeyRecord) error {
	if m.recs == nil {
		m.recs = map[int32]UserKeyRecord{}
	}
	m.recs[userID] = rec
	return nil
}

func userKeyTestStore(t *testing.T) (*Store, *memUserKeyStorage) {
	t.Helper()
	dir := t.TempDir()
	s := NewStore(&fileKeySource{path: filepath.Join(dir, "master.key")}, filepath.Join(dir, "keystore.json"))
	mem := &memUserKeyStorage{}
	s.SetUserKeyStorage(mem)
	return s, mem
}

func TestUserSealRoundtripAndFormat(t *testing.T) {
	s, _ := userKeyTestStore(t)
	if err := s.CreateUserKey(7, "user-password"); err != nil {
		t.Fatal(err)
	}
	sealed, err := s.SealForUser(7, "sk-user-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "enc:u1:7:") {
		t.Fatalf("wrong format: %q", sealed)
	}
	if !IsSealed(sealed) {
		t.Fatal("u1 values must count as sealed")
	}
	plain, err := s.Open(sealed)
	if err != nil || plain != "sk-user-secret" {
		t.Fatalf("roundtrip failed: %q, %v", plain, err)
	}
	// Empty and double-seal semantics match the root path.
	if v, _ := s.SealForUser(7, ""); v != "" {
		t.Fatal("empty must stay empty")
	}
	if v, _ := s.SealForUser(7, sealed); v != sealed {
		t.Fatal("sealing a sealed value must be a no-op")
	}
}

func TestUserSealIsolationBetweenUsers(t *testing.T) {
	s, mem := userKeyTestStore(t)
	if err := s.CreateUserKey(1, "pw-one"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUserKey(2, "pw-two"); err != nil {
		t.Fatal(err)
	}
	if mem.recs[1].WrappedDEKServer == mem.recs[2].WrappedDEKServer {
		t.Fatal("users must get distinct DEKs")
	}
	sealed, _ := s.SealForUser(1, "belongs-to-1")
	// Corrupt the embedded owner: pretend it's user 2's value.
	forged := strings.Replace(sealed, "enc:u1:1:", "enc:u1:2:", 1)
	if _, err := s.Open(forged); err == nil {
		t.Fatal("value sealed for user 1 must not open with user 2's DEK")
	}
}

func TestPasswordWrapVerifyAndRewrap(t *testing.T) {
	s, _ := userKeyTestStore(t)
	if err := s.CreateUserKey(3, "original-pw"); err != nil {
		t.Fatal(err)
	}
	if !s.VerifyUserPassword(3, "original-pw") {
		t.Fatal("correct password must verify")
	}
	if s.VerifyUserPassword(3, "wrong-pw") {
		t.Fatal("wrong password must not verify")
	}

	sealed, _ := s.SealForUser(3, "survives-rotation")

	// Forgot-password path: re-wrap via the server wrap, old password gone.
	if err := s.RewrapUserPassword(3, "new-pw"); err != nil {
		t.Fatal(err)
	}
	if !s.VerifyUserPassword(3, "new-pw") || s.VerifyUserPassword(3, "original-pw") {
		t.Fatal("rewrap must move the password wrap to the new password")
	}
	// The DEK itself is unchanged — existing secrets still open.
	if plain, err := s.Open(sealed); err != nil || plain != "survives-rotation" {
		t.Fatalf("secret lost across password rotation: %q, %v", plain, err)
	}
}

func TestCryptoShredding(t *testing.T) {
	s, mem := userKeyTestStore(t)
	if err := s.CreateUserKey(9, "pw"); err != nil {
		t.Fatal(err)
	}
	sealed, _ := s.SealForUser(9, "shred-me")

	delete(mem.recs, 9)
	s.SetUserKeyStorage(mem) // drop the record and the in-memory cache

	if _, err := s.Open(sealed); err == nil {
		t.Fatal("deleting the user key must make their secrets undecryptable")
	}
}

func TestSealForUserFallsBackWithoutKey(t *testing.T) {
	s, _ := userKeyTestStore(t)
	// No key created for user 5: value must still never be stored raw.
	sealed, err := s.SealForUser(5, "no-key-yet")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, Prefix) {
		t.Fatalf("expected root-DEK fallback, got %q", sealed)
	}
	if plain, err := s.Open(sealed); err != nil || plain != "no-key-yet" {
		t.Fatalf("fallback roundtrip failed: %q, %v", plain, err)
	}
}

func TestEnsureUserKeyIdempotent(t *testing.T) {
	s, mem := userKeyTestStore(t)
	if err := s.EnsureUserKey(4, "pw"); err != nil {
		t.Fatal(err)
	}
	first := mem.recs[4]
	if err := s.EnsureUserKey(4, "different-pw"); err != nil {
		t.Fatal(err)
	}
	if mem.recs[4] != first {
		t.Fatal("EnsureUserKey must not replace an existing key")
	}
}
