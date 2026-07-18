package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/secrets"
)

// UserSSHKeyFile is the per-user private-key path, materialized from the
// encrypted DB row for git operations. Kept separate per user so one account's
// key can never overwrite another's.
func (p Paths) UserSSHKeyFile(userID int32) string {
	return filepath.Join(p.SSHDir(), fmt.Sprintf("u%d", userID), "id_rsa")
}

// writeUserSSHKey materializes a decrypted key to the per-user path (0600) so
// `ssh -i` can read it, and returns the path.
func (p Paths) writeUserSSHKey(userID int32, key string) (string, error) {
	path := p.UserSSHKeyFile(userID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		return "", err
	}
	return path, nil
}

// ResolveSSHKeyPath returns the SSH key path to use for git operations on behalf
// of userID. It prefers the user's own key — decrypted from the DB and written
// to a 0600 file — when the user has one AND their vault is unlocked. Otherwise
// it falls back to the shared, operator-provisioned key (the historical single
// key), which covers local/dev and users who never uploaded a personal key.
//
// A user with a personal key whose vault is locked also falls back to the shared
// key; the per-user key is simply undecryptable until they re-authenticate.
func ResolveSSHKeyPath(ctx context.Context, q *db.Queries, base string, userID int32) string {
	paths := NewPaths(base)
	if userID != 0 && secrets.IsUnlocked(userID) {
		if cred, err := q.GetUserGitCredential(ctx, userID); err == nil && cred.SSHPrivateKey != "" {
			if path, err := paths.writeUserSSHKey(userID, cred.SSHPrivateKey); err == nil {
				return path
			}
		}
	}
	return paths.SSHKeyFile()
}

// ResolveSSHKeyPathForCompany resolves the git key for a company's pushes: the
// company creator's personal key (companies always carry their creator's
// UserID), else the shared key.
func ResolveSSHKeyPathForCompany(ctx context.Context, q *db.Queries, base string, company db.Company) string {
	var userID int32
	if company.UserID != nil {
		userID = *company.UserID
	}
	return ResolveSSHKeyPath(ctx, q, base, userID)
}
