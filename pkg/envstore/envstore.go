// Package envstore persists the environment variables a deploy delivers, and
// applies them to the process environment at startup.
//
// The point is that a server's configuration and secrets live in ONE place — the
// GitHub Environment the deploy came from — instead of being hand-edited on
// every box. CI reads the environment's vars/secrets, sends them with the deploy
// webhook, and the server stores them here. They are applied before anything
// reads configuration, and they survive both the exec-into-new-binary a deploy
// performs and an ordinary reboot, because the store is a file.
//
// # TRUST MODEL
//
// Accepting env vars over the deploy webhook makes that endpoint a
// config-write surface, and process environment alone is enough to execute
// code: LD_PRELOAD, PATH, NODE_OPTIONS (the app spawns MCP servers via npx),
// GIT_SSH_COMMAND, BASH_ENV, SSL_CERT_FILE. A delivered value must therefore
// never be able to reach those, or a leaked deploy key would escalate from
// "deploy a digest-pinned binary from our own repo" to arbitrary code. Nor may
// it reach the deploy system's own settings, which are what constrain the key in
// the first place — that loop is closed by refusing the HEADCOUNT1_DEPLOY_
// prefix outright, so deploy config stays on the box.
//
// CI delivers every variable and secret the environment exposes, so meeting such
// a name is ordinary rather than a misconfiguration: Partition() drops them and
// the controller reports the names back to CI, where the job log shows what did
// not make it. Apply() re-checks on the way out, so a hand-tampered store file
// cannot inject them either.
//
// The file is 0600 and lives next to settings.yaml, inside the data root that
// the agent sandbox hides from the untrusted shell. Values are stored in the
// clear, the same trust model as a systemd EnvironmentFile: anyone who can read
// the file already runs as the server user.
package envstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"agent-orchestrator/db"
)

// Limits on a delivered payload. Generous for real configuration, but bounded:
// the store is written from an authenticated-but-remote call, so it must not be
// an unbounded write.
const (
	MaxKeys       = 200
	MaxValueBytes = 64 * 1024
)

// keyPattern is deliberately uppercase-only. Beyond matching env-var
// convention, it is load-bearing: Go and the shells honour the lowercase
// spellings of several dangerous variables (http_proxy, ld_preload), and
// rejecting lowercase means the denylist below only has to name the uppercase
// form of each.
var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,126}$`)

// deniedPrefixes are refused with everything under them.
var deniedPrefixes = []struct{ prefix, reason string }{
	{"LD_", "the dynamic loader runs code from LD_PRELOAD/LD_AUDIT"},
	{"DYLD_", "the macOS dynamic loader runs code from DYLD_INSERT_LIBRARIES"},
	{"GIT_", "git runs commands from GIT_SSH_COMMAND/GIT_EXTERNAL_DIFF/GIT_PAGER"},
	{"HEADCOUNT1_DEPLOY_", "deploy configuration is what constrains a deploy; it stays on the server"},
	{"HEADCOUNT1_SANDBOX_", "server-local sandbox switch"},
}

// deniedKeys are refused by exact name, each because setting it either executes
// code, redirects the server's trust, or relaxes a security control.
var deniedKeys = map[string]string{
	"PATH":                               "changes which binaries the server and agent shells execute",
	"HOME":                               "moves the data root, including this store",
	"SHELL":                              "changes the interpreter agent shell commands run under",
	"IFS":                                "changes shell word splitting",
	"ENV":                                "sourced by shells at startup",
	"BASH_ENV":                           "sourced by bash at startup",
	"CDPATH":                             "changes what `cd` resolves to in agent shells",
	"NODE_OPTIONS":                       "node runs code from --require/--import, and the app spawns MCP servers via npx",
	"PYTHONPATH":                         "changes which python modules are imported",
	"PYTHONSTARTUP":                      "python executes it on startup",
	"PYTHONHOME":                         "relocates the python runtime",
	"PERL5OPT":                           "perl runs code from it",
	"RUBYOPT":                            "ruby runs code from it",
	"HTTP_PROXY":                         "redirects the server's outbound traffic",
	"HTTPS_PROXY":                        "redirects the server's outbound traffic",
	"ALL_PROXY":                          "redirects the server's outbound traffic",
	"NO_PROXY":                           "redirects the server's outbound traffic",
	"SSL_CERT_FILE":                      "replaces the TLS trust store",
	"SSL_CERT_DIR":                       "replaces the TLS trust store",
	"SSH_AUTH_SOCK":                      "points ssh at another agent socket",
	"SSH_AGENT_PID":                      "points ssh at another agent",
	"HEADCOUNT1_ENV":                     "this server's own identity decides which deploy events it accepts",
	"HEADCOUNT1_ENABLE_GLOBAL_ADMIN_API": "server-local security switch",
	"HEADCOUNT1_GIT_STRICT_HOST_KEY_CHECKING": "server-local security switch",
	"E2E_MODE":            "test-mode switch",
	"E2E_HEADCOUNT1_HOME": "decides where this store itself lives",
	"RECORD":              "test-mode switch",
}

// Store is the persisted form: the delivered values, which of their names came
// from GitHub *secrets* rather than plain variables, and when they landed.
type Store struct {
	Values map[string]string `json:"values"`
	// SecretKeys lets the agent sandbox scrub delivered secrets from the
	// untrusted shell's environment by name, instead of relying on its
	// best-effort "looks secret-ish" denylist to guess (see tools.scrubbedEnv).
	SecretKeys []string `json:"secret_keys,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

// Path is the store's location: alongside settings.yaml, in the bootstrap data
// root, because it has to be readable before any other configuration.
func Path() string {
	return filepath.Join(db.Headcount1Home(), "deploy-env.json")
}

// Validate reports whether a single key may be delivered.
func Validate(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("not a valid environment variable name (expected UPPER_SNAKE_CASE)")
	}
	if reason, denied := deniedKeys[key]; denied {
		return fmt.Errorf("refused: %s", reason)
	}
	for _, d := range deniedPrefixes {
		if strings.HasPrefix(key, d.prefix) {
			return fmt.Errorf("refused: %s", d.reason)
		}
	}
	return nil
}

// Partition splits a delivered payload into what will be applied and what will
// not, with a reason per rejected key.
//
// Skipping rather than refusing the whole payload is deliberate. CI delivers
// EVERY variable and secret the GitHub Environment exposes, so a repository
// secret that happens to be named GIT_TOKEN, or an unrelated PATH variable, is a
// normal thing to encounter — not a misconfiguration to fail the deploy over.
// The caller reports the skipped names back to CI so they are visible in the job
// log rather than silently disappearing.
func Partition(values map[string]string) (accepted map[string]string, skipped map[string]string) {
	accepted = make(map[string]string, len(values))
	skipped = map[string]string{}
	for key, val := range values {
		if err := Validate(key); err != nil {
			skipped[key] = err.Error()
			continue
		}
		if len(val) > MaxValueBytes {
			skipped[key] = fmt.Sprintf("value is %d bytes (max %d)", len(val), MaxValueBytes)
			continue
		}
		accepted[key] = val
	}
	return accepted, skipped
}

// SkippedSummary renders Partition's skipped map as sorted "KEY: reason" lines.
func SkippedSummary(skipped map[string]string) []string {
	out := make([]string, 0, len(skipped))
	for key, reason := range skipped {
		out = append(out, key+": "+reason)
	}
	sort.Strings(out)
	return out
}

// Digest identifies the delivered set, so a deploy can tell whether the
// configuration changed even when the binary did not — see the deploy
// controller, which restarts on an env-only change so the new values take
// effect. Nil and empty share a digest: both mean "nothing delivered".
func (s Store) Digest() string {
	keys := make([]string, 0, len(s.Values))
	for k := range s.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		// Length-prefixed, so no combination of names and values can be
		// rearranged into the same byte stream.
		fmt.Fprintf(h, "%d:%s=%d:%s\n", len(k), k, len(s.Values[k]), s.Values[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Keys returns the delivered names, sorted. Values are never exposed.
func (s Store) Keys() []string {
	keys := make([]string, 0, len(s.Values))
	for k := range s.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Load reads the store. A missing file is not an error — it just means no
// deploy has delivered configuration yet.
func Load() (Store, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return Store{}, nil
		}
		return Store{}, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return Store{}, fmt.Errorf("parse %s: %w", Path(), err)
	}
	return s, nil
}

// Save replaces the store atomically, 0600. Written via a temp file + rename so
// a crash mid-write cannot leave a half-parsed store that would boot the server
// with partial configuration.
func Save(s Store) error {
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	sort.Strings(s.SecretKeys)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Apply sets every stored variable in the current process environment and
// returns the names it applied, sorted.
//
// Delivered values WIN over anything already in the environment: the GitHub
// Environment is the intended source of truth, so a value set there must not be
// silently shadowed by a stale one in a systemd unit. Startup logs the names
// applied (never the values) so the precedence is visible when a value
// surprises someone.
//
// Keys are re-checked here, not just on receipt, so a store file edited by hand
// — or written by an older, more permissive build — still cannot inject PATH.
func Apply() ([]string, error) {
	s, err := Load()
	if err != nil {
		return nil, err
	}
	applied := make([]string, 0, len(s.Values))
	for _, key := range s.Keys() {
		if err := Validate(key); err != nil {
			return applied, fmt.Errorf("stored key %s is not applicable: %w", key, err)
		}
		if err := os.Setenv(key, s.Values[key]); err != nil {
			return applied, fmt.Errorf("set %s: %w", key, err)
		}
		applied = append(applied, key)
	}
	return applied, nil
}
