// Package deploysync pushes an environment's entries (secrets + variables)
// to external deploy targets, so a deployment made on that target picks up
// the current values. Two providers are supported:
//
//   - Vercel: project environment variables (plain for variables, sensitive/
//     encrypted for secrets) via the REST API.
//   - GitHub: repo *environment*-scoped Actions variables and secrets;
//     secret values are sealed client-side with libsodium's crypto_box_seal
//     (x/crypto nacl/box SealAnonymous) against the environment public key,
//     exactly as the GitHub API requires.
//
// Sync is push-only by design. Reading values back is not generally possible
// (GitHub secrets and Vercel sensitive vars are write-only on the target), so
// this system remains the source of truth.
package deploysync

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Entry is one env entry to push. Kind is db.EnvEntrySecret or
// db.EnvEntryVariable (string-typed here to avoid an import cycle).
type Entry struct {
	Name  string
	Value string
	Kind  string // "secret" | "variable"
}

// RemoteEntry is one entry as it exists on the deploy target. Only the NAME
// is generally available: secret values are write-only on both providers
// (GitHub Actions secrets, Vercel sensitive vars), so a remote entry that is
// missing locally can be displayed and overwritten, never read.
type RemoteEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "secret" | "variable"
}

// Target pushes entries to one connected deploy environment.
type Target interface {
	// PushAll upserts every entry on the target.
	PushAll(ctx context.Context, entries []Entry) error
	// Delete removes one entry by name from the target. Unknown names are
	// not an error (the entry may never have been pushed).
	Delete(ctx context.Context, name string) error
	// ListRemote returns the names (and kinds) of the entries currently on
	// the target's environment.
	ListRemote(ctx context.Context) ([]RemoteEntry, error)
}

// httpClient is shared by both providers: short timeout, no redirects into
// the unknown.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// baseURL resolves a provider API root, overridable via env for tests
// (e.g. VERCEL_API_URL / GITHUB_API_URL pointing at a mock server).
func baseURL(envVar, def string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return def
}

func httpError(op string, resp *http.Response, body []byte) error {
	b := string(body)
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Errorf("%s: %s: %s", op, resp.Status, b)
}
