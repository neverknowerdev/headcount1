// Package updater applies a server-side deploy: it downloads a prebuilt binary
// for a given git ref (published by CI as a GitHub release asset), verifies it,
// and swaps the running executable for it, then asks the process to shut down
// gracefully so it can exec into the new build. The trigger is an authenticated
// deploy webhook (see the deploy controller), not client-side polling — the
// server is the source of truth for what it runs, and CI pushes deploy events
// to it.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// defaultDeployRepo is the repository a deploy binary must come from. A deploy
// URL that points anywhere else is rejected, so a leaked deploy key cannot make
// the server fetch and execute a binary from an attacker's repository.
const defaultDeployRepo = "neverknowerdev/headcount1"

// VersionInfo identifies a build. The running build's values are stamped in at
// compile time via -ldflags; a deploy target's values arrive in the webhook.
//
// Version is the human-facing version number minted at build time (see
// scripts/version.sh) — "2026.07.29" in production, "staging-<branch>-<commit>"
// on staging. The remaining fields are the precise build identity, which is what
// deploy decisions actually compare.
type VersionInfo struct {
	Version    string `json:"version"`
	Branch     string `json:"branch"`
	CommitHash string `json:"commit_hash"`
	BuildDate  string `json:"build_date"`
}

// DisplayString is the unambiguous build identity used in logs and operator
// output. It stays branch+date+commit rather than the version number: two
// builds can share a version (a re-run of the same commit on another branch),
// but never this triple.
func (v VersionInfo) DisplayString() string {
	if v.CommitHash == "" || v.CommitHash == "unknown" {
		return "dev"
	}
	return fmt.Sprintf("%s+%s+%s", v.Branch, v.BuildDate, v.CommitHash)
}

// Status is the deploy state exposed to the UI: the build currently running,
// and whether a deploy is in progress / last failed.
type Status struct {
	Current   VersionInfo `json:"current"`
	Deploying bool        `json:"deploying"`
	LastError string      `json:"last_error,omitempty"`
	// LastDeploy is the version the in-progress (or most recent) deploy is
	// switching to — what this server will report as Current once it has
	// exec'd into the new binary.
	LastDeploy *VersionInfo `json:"last_deploy,omitempty"`
}

type Updater struct {
	mu        sync.RWMutex
	current   VersionInfo
	status    Status
	deploying bool
	// restartPending/pendingExecPath are set once a deploy has successfully
	// replaced the executable on disk. main's shutdown sequence reads them via
	// RestartPending and execs the new binary as its final act.
	restartPending  bool
	pendingExecPath string
	// downloadTokenFn returns a bearer token for fetching the binary from a
	// private release asset, or "" for a public download.
	downloadTokenFn func() string
}

// New creates an Updater for the running build. downloadTokenFn may be nil.
func New(version, branch, commitHash, buildDate string, downloadTokenFn func() string) *Updater {
	if downloadTokenFn == nil {
		downloadTokenFn = func() string { return "" }
	}
	current := VersionInfo{Version: version, Branch: branch, CommitHash: commitHash, BuildDate: buildDate}
	return &Updater{
		current:         current,
		status:          Status{Current: current},
		downloadTokenFn: downloadTokenFn,
	}
}

// Current returns the running build's version.
func (u *Updater) Current() VersionInfo {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.current
}

// GetStatus returns a snapshot of the deploy state.
func (u *Updater) GetStatus() Status {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status
}

// IsCurrent reports whether the running build already IS the given target, so a
// deploy event for the commit we're already on can be ignored (no restart loop).
func (u *Updater) IsCurrent(target VersionInfo) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return target.CommitHash != "" && target.CommitHash == u.current.CommitHash
}

// RestartPending reports whether a deploy has already replaced this process's
// executable on disk, and the absolute path to exec. main calls it at the end
// of its shutdown sequence.
func (u *Updater) RestartPending() (string, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.pendingExecPath, u.restartPending
}

// Deploy downloads the binary at downloadURL, verifies it against sha256Hex,
// replaces the running executable with it, and requests a graceful shutdown by
// signalling SIGTERM to this process. target is used for status and logging.
//
// It deliberately does NOT start the successor process. main's shutdown handler
// execs the new binary as its final act — after in-flight agent runs have been
// drained and persisted, the keyring sealed, and the HTTP listener closed (see
// RestartPending). Spawning a successor here instead would (a) race the
// outgoing process for the listening port, which it holds for the whole drain
// window, and (b) run the successor's interrupted-run resume scan before the
// outgoing process had written the runs it paused, stranding them.
//
// Concurrent deploys are rejected: the first one wins and the process is on its
// way out, so a second is meaningless.
func (u *Updater) Deploy(downloadURL, sha256Hex string, target VersionInfo) error {
	u.mu.Lock()
	if u.deploying {
		u.mu.Unlock()
		return errors.New("a deploy is already in progress")
	}
	u.deploying = true
	u.status.Deploying = true
	u.status.LastError = ""
	u.mu.Unlock()

	fail := func(err error) error {
		u.mu.Lock()
		u.deploying = false
		u.status.Deploying = false
		u.status.LastError = err.Error()
		u.mu.Unlock()
		return err
	}

	// Validate BEFORE fetching: the deploy key is a shared secret, so the URL
	// it authorizes must still be constrained to our own release artifacts.
	if err := ValidateDownloadURL(downloadURL); err != nil {
		return fail(fmt.Errorf("deploy: %w", err))
	}
	// Pin the exact artifact. Without this, anything ever published to the repo
	// (including an older, vulnerable build) would be a valid deploy target.
	expected := strings.ToLower(strings.TrimSpace(sha256Hex))
	if expected == "" {
		return fail(errors.New("deploy: sha256 is required"))
	}

	log.Printf("Deploy: downloading %s (%s)...", target.DisplayString(), downloadURL)
	tmpFile, gotSum, err := downloadBinary(downloadURL, u.downloadTokenFn())
	if err != nil {
		return fail(fmt.Errorf("deploy: download binary: %w", err))
	}
	if gotSum != expected {
		os.Remove(tmpFile)
		return fail(fmt.Errorf("deploy: sha256 mismatch: expected %s, got %s", expected, gotSum))
	}

	execPath, err := os.Executable()
	if err != nil {
		os.Remove(tmpFile)
		return fail(fmt.Errorf("deploy: get executable path: %w", err))
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		os.Remove(tmpFile)
		return fail(fmt.Errorf("deploy: eval symlinks: %w", err))
	}

	if err := os.Rename(tmpFile, execPath); err != nil {
		// Rename fails across filesystems; fall back to an in-place copy.
		if copyErr := copyFile(tmpFile, execPath); copyErr != nil {
			os.Remove(tmpFile)
			return fail(fmt.Errorf("deploy: replace binary: %w (copy also failed: %v)", err, copyErr))
		}
		os.Remove(tmpFile)
	}

	u.mu.Lock()
	t := target
	u.status.LastDeploy = &t
	u.restartPending = true
	u.pendingExecPath = execPath
	u.mu.Unlock()

	log.Printf("Deploy: binary replaced; draining and restarting into %s...", target.DisplayString())
	u.requestShutdown()
	return nil
}

// RestartInPlace restarts into the SAME binary, gracefully. It exists for
// delivered configuration: environment variables are read once at startup, so a
// deploy that changes only the env (same commit) still has to cycle the process
// for the new values to take effect.
//
// Like Deploy it goes through main's shutdown path rather than exiting, so
// in-flight agent runs are drained and resumed and the keyring is sealed. reason
// is logged so an unexplained restart isn't a mystery in the log.
func (u *Updater) RestartInPlace(reason string) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: get executable path: %w", err)
	}
	if execPath, err = filepath.EvalSymlinks(execPath); err != nil {
		return fmt.Errorf("restart: eval symlinks: %w", err)
	}

	u.mu.Lock()
	if u.restartPending {
		// A deploy already armed a restart; that one supersedes this.
		u.mu.Unlock()
		return nil
	}
	u.restartPending = true
	u.pendingExecPath = execPath
	u.mu.Unlock()

	log.Printf("Restart: %s; draining and restarting %s...", reason, execPath)
	u.requestShutdown()
	return nil
}

// requestShutdown asks this process to shut down gracefully, so main's SIGTERM
// handler can seal the secrets keyring, drain in-flight agent runs so they
// resume in the successor, close the listener, and exec last of all.
func (u *Updater) requestShutdown() {
	go func() {
		time.Sleep(500 * time.Millisecond)
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		} else {
			os.Exit(0)
		}
	}()
}

// allowedDownloadHosts is the set of hosts a deploy binary may be fetched from.
// Operators can override it with HEADCOUNT1_DEPLOY_ALLOWED_HOSTS (comma
// separated) to point at a private mirror; overriding also relaxes the
// repository-path check below, since a mirror's layout is its own.
func allowedDownloadHosts() (hosts map[string]bool, overridden bool) {
	if custom := strings.TrimSpace(os.Getenv("HEADCOUNT1_DEPLOY_ALLOWED_HOSTS")); custom != "" {
		hosts = map[string]bool{}
		for _, h := range strings.Split(custom, ",") {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				hosts[h] = true
			}
		}
		return hosts, true
	}
	return map[string]bool{
		"api.github.com":                       true,
		"github.com":                           true,
		"objects.githubusercontent.com":        true,
		"release-assets.githubusercontent.com": true,
	}, false
}

// deployRepo is the "owner/name" a deploy URL must reference.
func deployRepo() string {
	if r := strings.TrimSpace(os.Getenv("HEADCOUNT1_DEPLOY_REPO")); r != "" {
		return r
	}
	return defaultDeployRepo
}

// ValidateDownloadURL enforces that a deploy binary comes from an allowed host
// and, on GitHub, from our own repository. The deploy webhook's shared key
// authenticates the CALLER; this constrains what that caller can make the
// server execute, so a leaked key is not by itself remote code execution.
func ValidateDownloadURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("download_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable download_url: %w", err)
	}
	hosts, overridden := allowedDownloadHosts()
	// Plain http is only tolerated for an explicitly configured host (a local
	// mirror or a test fixture); the GitHub defaults are https-only.
	if u.Scheme != "https" && !(u.Scheme == "http" && overridden) {
		return fmt.Errorf("download_url must use https (got %q)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if !hosts[host] {
		return fmt.Errorf("download host %q is not an allowed deploy source", host)
	}
	// api.github.com and github.com URLs name the repository in their path
	// (/repos/{owner}/{repo}/... and /{owner}/{repo}/... respectively), so pin
	// it. The githubusercontent CDN hosts opaque signed blob paths and is only
	// ever reached by following a redirect from an already-validated API URL.
	if !overridden && (host == "api.github.com" || host == "github.com") {
		if !strings.Contains(u.Path, "/"+deployRepo()+"/") {
			return fmt.Errorf("download_url does not belong to %s", deployRepo())
		}
	}
	return nil
}

// downloadBinary fetches url to a temp file, returning the path and the file's
// hex-encoded sha256 (computed in the same pass, so the bytes are never read
// twice and the digest is of exactly what was written).
func downloadBinary(url, token string) (path string, sha256Hex string, err error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "headcount1-updater")
	// GitHub release-asset API URLs need this Accept header to return the raw
	// binary; it's harmless for a plain browser_download_url too.
	req.Header.Set("Accept", "application/octet-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "headcount1-deploy-*")
	if err != nil {
		return "", "", err
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, hasher), resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", "", err
	}
	tmpFile.Close()

	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		os.Remove(tmpFile.Name())
		return "", "", err
	}
	return tmpFile.Name(), hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(0755)
}
