package secrets

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// PrefixUser marks a value sealed with a specific user's DEK. The owning
// user's ID is embedded in the value ("enc:u1:<userID>:<base64>") so that
// decryption is self-describing — it never depends on which struct or column
// the ciphertext came from.
const PrefixUser = "enc:u1:"

// UserKeyRecord is the persisted shape of a user's key material. All fields
// are safe at rest: the DEK only ever appears wrapped, twice —
//   - WrappedDEKServer: AES-GCM under the install's root DEK, so background
//     work (agent runs, schedulers, the LLM proxy) can decrypt with nobody
//     logged in, and so a password reset can re-wrap without data loss;
//   - WrappedDEKPassword: AES-GCM under Argon2id(password, PwSalt, PwParams),
//     verified at login and re-created on password change.
type UserKeyRecord struct {
	WrappedDEKServer   string
	WrappedDEKPassword string
	PwSalt             string
	PwParams           string
}

// UserKeyStorage abstracts where UserKeyRecords live (the db package's
// user_keys table in production). Injected via SetUserKeyStorage to avoid an
// import cycle (db already imports this package for the serializer).
//
// IMPORTANT: decryption happens inside GORM's row Scan, while the (possibly
// only — SQLite) DB connection is held, so the decrypt path must never
// trigger a storage query: all records are hydrated into memory when the
// storage is installed (ListUserKeys) and kept fresh on every save. Direct
// GetUserKey lookups are reserved for login-side paths that run outside row
// scans.
type UserKeyStorage interface {
	GetUserKey(userID int32) (UserKeyRecord, error)
	ListUserKeys() (map[int32]UserKeyRecord, error)
	SaveUserKey(userID int32, rec UserKeyRecord) error
}

// argonParamsV1 is the current password-KDF setting (RFC 9106 second
// recommendation: 64 MiB, t=3 reduced to t=1 with p=4 per the first). The
// string is stored per-record so parameters can evolve without breaking old
// wraps.
const argonParamsV1 = "argon2id$v=1$t=1$m=65536$p=4"

func (s *Store) SetUserKeyStorage(st UserKeyStorage) {
	var cache map[int32]UserKeyRecord
	if st != nil {
		// Hydrate every record up front — the decrypt path is cache-only
		// (see UserKeyStorage doc).
		if recs, err := st.ListUserKeys(); err == nil {
			cache = recs
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userKeys = st
	s.userKeyCache = cache
}

// SetUserKeyStorage installs the production storage on the default store.
func SetUserKeyStorage(st UserKeyStorage) { Default().SetUserKeyStorage(st) }

// CreateUserKey generates a fresh DEK for a user and persists it under both
// wraps. Called at registration (and lazily at login for accounts that
// predate per-user keys).
func (s *Store) CreateUserKey(userID int32, password string) error {
	var dek [32]byte
	if _, err := rand.Read(dek[:]); err != nil {
		return err
	}
	return s.saveUserKeyWraps(userID, dek, password)
}

// EnsureUserKey creates a key for the user if none exists yet. The password
// is only used when creating; an existing key is left untouched. Runs on
// login paths, so it may consult storage directly (never overwrite a key
// another process created).
func (s *Store) EnsureUserKey(userID int32, password string) error {
	if _, err := s.userKeyRecordFresh(userID); err == nil {
		return nil
	}
	return s.CreateUserKey(userID, password)
}

// VerifyUserPassword reports whether the password opens the user's password
// wrap — an integrity check run at login, independent of the bcrypt hash.
func (s *Store) VerifyUserPassword(userID int32, password string) bool {
	_, err := s.userDEKViaPassword(userID, password)
	return err == nil
}

// RewrapUserPassword re-wraps the user's DEK under a new password. The DEK is
// recovered via the server wrap, so this works both for an authenticated
// password change and for a forgot-password reset — no data is ever lost.
func (s *Store) RewrapUserPassword(userID int32, newPassword string) error {
	rec, err := s.userKeyRecordFresh(userID)
	if err != nil {
		return err
	}
	dek, err := s.unwrapServer(userID, rec)
	if err != nil {
		return err
	}
	return s.saveUserKeyWraps(userID, dek, newPassword)
}

// SealForUser encrypts a secret under the user's DEK. If the user has no key
// (accounts predating per-user keys, fixture users), it falls back to the
// root-DEK format so the value is still never stored raw; it will be sealed
// per-user on its next write after the key exists.
func (s *Store) SealForUser(userID int32, plaintext string) (string, error) {
	if plaintext == "" || IsSealed(plaintext) {
		return plaintext, nil
	}
	dek, err := s.userDEK(userID)
	if err != nil {
		// No key for this user (or no user-key storage at all — tests,
		// stripped-down deployments): fall back to the root DEK so the value
		// is still never stored raw; it gets re-sealed per-user on its next
		// write once the key exists.
		return s.Seal(plaintext)
	}
	blob, err := gcmSeal(dek, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d:%s", PrefixUser, userID, base64.StdEncoding.EncodeToString(blob)), nil
}

// openUser decrypts an "enc:u1:" value using the DEK of the user embedded in
// the value itself.
func (s *Store) openUser(stored string) (string, error) {
	rest := strings.TrimPrefix(stored, PrefixUser)
	idStr, b64, ok := strings.Cut(rest, ":")
	if !ok {
		return "", fmt.Errorf("secrets: malformed user-sealed value")
	}
	userID, err := strconv.ParseInt(idStr, 10, 32)
	if err != nil {
		return "", fmt.Errorf("secrets: malformed user id in sealed value: %w", err)
	}
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("secrets: malformed user-sealed value: %w", err)
	}
	dek, err := s.userDEK(int32(userID))
	if err != nil {
		return "", err
	}
	plain, err := gcmOpen(dek, blob)
	if err != nil {
		return "", fmt.Errorf("secrets: cannot decrypt secret of user %d (key deleted or master key changed?): %w", userID, err)
	}
	return string(plain), nil
}

// ── user DEK resolution ─────────────────────────────────────────────────────

func (s *Store) userKeysConfigured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userKeys != nil
}

// userKeyRecord returns a user's record from the in-memory cache ONLY. The
// decrypt path runs inside GORM row scans while the (possibly single) DB
// connection is held — querying storage here would deadlock on SQLite. The
// cache is hydrated at SetUserKeyStorage time and updated on every save, so
// in a single-process deployment it is always complete. Only wrapped
// material is cached — unwrapping still walks the master-key chain on every
// operation, so key revocation at the source keeps its effect.
func (s *Store) userKeyRecord(userID int32) (UserKeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userKeys == nil {
		return UserKeyRecord{}, fmt.Errorf("secrets: user key storage not configured")
	}
	if rec, ok := s.userKeyCache[userID]; ok {
		return rec, nil
	}
	return UserKeyRecord{}, fmt.Errorf("secrets: no encryption key for user %d", userID)
}

// userKeyRecordFresh is the login-side variant: on a cache miss it consults
// storage directly (safe outside row scans) so keys created by another
// process are picked up.
func (s *Store) userKeyRecordFresh(userID int32) (UserKeyRecord, error) {
	if rec, err := s.userKeyRecord(userID); err == nil {
		return rec, nil
	}
	s.mu.Lock()
	st := s.userKeys
	s.mu.Unlock()
	if st == nil {
		return UserKeyRecord{}, fmt.Errorf("secrets: user key storage not configured")
	}
	rec, err := st.GetUserKey(userID)
	if err != nil {
		return UserKeyRecord{}, err
	}
	s.mu.Lock()
	if s.userKeyCache == nil {
		s.userKeyCache = map[int32]UserKeyRecord{}
	}
	s.userKeyCache[userID] = rec
	s.mu.Unlock()
	return rec, nil
}

// userDEK unwraps a user's DEK via the server wrap (root DEK chain).
// Cache-only record lookup — safe inside GORM row scans.
func (s *Store) userDEK(userID int32) ([32]byte, error) {
	rec, err := s.userKeyRecord(userID)
	if err != nil {
		return [32]byte{}, err
	}
	return s.unwrapServer(userID, rec)
}

// unwrapServer opens a record's server wrap with the root DEK chain.
func (s *Store) unwrapServer(userID int32, rec UserKeyRecord) ([32]byte, error) {
	var zero [32]byte
	wrapped, err := base64.StdEncoding.DecodeString(rec.WrappedDEKServer)
	if err != nil {
		return zero, fmt.Errorf("secrets: corrupt server wrap for user %d: %w", userID, err)
	}
	root, err := s.dek()
	if err != nil {
		return zero, err
	}
	dekBytes, err := gcmOpen(root, wrapped)
	if err != nil || len(dekBytes) != 32 {
		return zero, fmt.Errorf("secrets: failed to unwrap DEK of user %d: %w", userID, err)
	}
	var dek [32]byte
	copy(dek[:], dekBytes)
	return dek, nil
}

// userDEKViaPassword unwraps a user's DEK using their password wrap. Login
// path — may consult storage on a cache miss.
func (s *Store) userDEKViaPassword(userID int32, password string) ([32]byte, error) {
	var zero [32]byte
	rec, err := s.userKeyRecordFresh(userID)
	if err != nil {
		return zero, err
	}
	salt, err := base64.StdEncoding.DecodeString(rec.PwSalt)
	if err != nil {
		return zero, fmt.Errorf("secrets: corrupt salt for user %d: %w", userID, err)
	}
	kek, err := deriveArgonKey(password, salt, rec.PwParams)
	if err != nil {
		return zero, err
	}
	wrapped, err := base64.StdEncoding.DecodeString(rec.WrappedDEKPassword)
	if err != nil {
		return zero, fmt.Errorf("secrets: corrupt password wrap for user %d: %w", userID, err)
	}
	dekBytes, err := gcmOpen(kek, wrapped)
	if err != nil || len(dekBytes) != 32 {
		return zero, fmt.Errorf("secrets: password does not open the key of user %d", userID)
	}
	var dek [32]byte
	copy(dek[:], dekBytes)
	return dek, nil
}

// saveUserKeyWraps persists both wraps of the given DEK and refreshes the
// cache entry.
func (s *Store) saveUserKeyWraps(userID int32, dek [32]byte, password string) error {
	s.mu.Lock()
	st := s.userKeys
	s.mu.Unlock()
	if st == nil {
		return fmt.Errorf("secrets: user key storage not configured")
	}

	root, err := s.dek()
	if err != nil {
		return err
	}
	serverWrap, err := gcmSeal(root, dek[:])
	if err != nil {
		return err
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	pwKey, err := deriveArgonKey(password, salt, argonParamsV1)
	if err != nil {
		return err
	}
	pwWrap, err := gcmSeal(pwKey, dek[:])
	if err != nil {
		return err
	}

	rec := UserKeyRecord{
		WrappedDEKServer:   base64.StdEncoding.EncodeToString(serverWrap),
		WrappedDEKPassword: base64.StdEncoding.EncodeToString(pwWrap),
		PwSalt:             base64.StdEncoding.EncodeToString(salt),
		PwParams:           argonParamsV1,
	}
	if err := st.SaveUserKey(userID, rec); err != nil {
		return err
	}
	s.mu.Lock()
	if s.userKeyCache == nil {
		s.userKeyCache = map[int32]UserKeyRecord{}
	}
	s.userKeyCache[userID] = rec
	s.mu.Unlock()
	return nil
}

// deriveArgonKey derives a 32-byte wrapping key from a password using the
// parameters recorded alongside the wrap (so parameter upgrades never break
// existing records).
func deriveArgonKey(password string, salt []byte, params string) ([32]byte, error) {
	var key [32]byte
	kind, rest, _ := strings.Cut(params, "$")
	if subtle.ConstantTimeCompare([]byte(kind), []byte("argon2id")) != 1 {
		return key, fmt.Errorf("secrets: unsupported password KDF %q", kind)
	}
	t, m, p := uint32(1), uint32(64*1024), uint8(4)
	for _, part := range strings.Split(rest, "$") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return key, fmt.Errorf("secrets: bad KDF params %q", params)
		}
		switch k {
		case "t":
			t = uint32(n)
		case "m":
			m = uint32(n)
		case "p":
			p = uint8(n)
		}
	}
	derived := argon2.IDKey([]byte(password), salt, t, m, p, 32)
	copy(key[:], derived)
	return key, nil
}
