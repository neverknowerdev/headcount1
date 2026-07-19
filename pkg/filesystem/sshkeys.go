package filesystem

import (
	"context"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"
)

// noopCleanup is returned when nothing was materialized (the shared key path).
func noopCleanup() {}

// materializeEphemeralKey writes a decrypted key to a FRESH 0700 temp dir under
// <base>/ssh and returns its path plus a cleanup that removes it. The plaintext
// key must not outlive the git operation that needs it — a persistent per-user
// file would survive logout, vault-lock, and crypto-shred, defeating the
// at-rest-encryption and crypto-shred guarantees. Callers defer the cleanup.
func materializeEphemeralKey(base, key string) (string, func(), error) {
	sshDir := NewPaths(base).SSHDir()
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return "", noopCleanup, err
	}
	dir, err := os.MkdirTemp(sshDir, "key-*")
	if err != nil {
		return "", noopCleanup, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		os.RemoveAll(dir)
		return "", noopCleanup, err
	}
	path := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		os.RemoveAll(dir)
		return "", noopCleanup, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

// ResolveSSHKeyPath returns the SSH key path to use for git operations on behalf
// of userID, plus a cleanup func the caller MUST defer. When the user has their
// own key AND their vault is unlocked, the key is decrypted and written to a
// short-lived 0700 temp file that the cleanup deletes right after the git op —
// so the plaintext never lingers on disk. Otherwise it falls back to the shared,
// operator-provisioned key (persistent; cleanup is a no-op), which covers
// local/dev and users who never uploaded a personal key.
//
// A user with a personal key whose vault is locked also falls back to the shared
// key; the per-user key is simply undecryptable until they re-authenticate.
func ResolveSSHKeyPath(ctx context.Context, q *db.Queries, base string, userID int32) (string, func()) {
	paths := NewPaths(base)
	if userID != 0 && secrets.IsUnlocked(userID) {
		if cred, err := q.GetUserGitCredential(ctx, userID); err == nil && cred.SSHPrivateKeyEncrypted != "" {
			// Decrypt at the point of use — the plaintext lives only for the
			// lifetime of the returned cleanup.
			if key, err := secrets.Default().Decrypt(cred.SSHPrivateKeyEncrypted); err == nil && key != "" {
				if path, cleanup, err := materializeEphemeralKey(base, key); err == nil {
					return path, cleanup
				}
			}
		}
	}
	return paths.SSHKeyFile(), noopCleanup
}

// ResolveSSHKeyPathForCompany resolves the git key for a company's pushes: the
// company creator's personal key (companies always carry their creator's
// UserID), else the shared key. The returned cleanup MUST be deferred.
func ResolveSSHKeyPathForCompany(ctx context.Context, q *db.Queries, base string, company db.Company) (string, func()) {
	var userID int32
	if company.UserID != nil {
		userID = *company.UserID
	}
	return ResolveSSHKeyPath(ctx, q, base, userID)
}
