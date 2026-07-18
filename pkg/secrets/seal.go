package secrets

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"
)

// KeyUnwrapper seals and unseals a small blob under a boot key that lives
// outside the process — a cloud KMS, a Vault/OpenBao Transit engine, or an
// env-provided key. It is used ONLY to protect the graceful-exit keyring
// snapshot so a planned restart re-warms without every user re-tapping their
// passkey. It never decrypts at-rest user secrets on its own: in steady state
// the snapshot does not exist, so possessing the boot key reveals nothing.
type KeyUnwrapper interface {
	Name() string
	Seal(plaintext []byte) ([]byte, error)
	Unseal(ciphertext []byte) ([]byte, error)
}

// SealKeyring serializes the live keyring (userID → DEK) and seals it under
// the boot key, for writing to disk on a graceful shutdown. Returns an empty
// blob (and nil error) when no user is unlocked, so callers can skip the write.
func (s *Store) SealKeyring(u KeyUnwrapper) ([]byte, error) {
	snap := s.keyring.Snapshot()
	if len(snap) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(snap))
	for id, dek := range snap {
		m[strconv.Itoa(int(id))] = base64.StdEncoding.EncodeToString(dek[:])
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return u.Seal(data)
}

// UnsealKeyring restores a sealed keyring snapshot on startup, giving each
// entry a fresh ttl. The caller deletes the on-disk blob afterwards so an
// unexpected crash can't replay a stale keyring.
func (s *Store) UnsealKeyring(u KeyUnwrapper, blob []byte, ttl time.Duration) error {
	data, err := u.Unseal(blob)
	if err != nil {
		return err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	out := make(map[int32][32]byte, len(m))
	for k, v := range m {
		id, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(b) != 32 {
			continue
		}
		var dek [32]byte
		copy(dek[:], b)
		out[int32(id)] = dek
	}
	s.keyring.Restore(out, ttl)
	return nil
}
