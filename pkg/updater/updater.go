// Package updater applies a server-side deploy: it downloads a prebuilt binary
// for a given git ref (published by CI as a GitHub release asset) and swaps the
// running executable for it, then triggers a graceful restart so the new binary
// takes over. The trigger is an authenticated deploy webhook (see the deploy
// controller), not client-side polling — the server is the source of truth for
// what it runs, and CI pushes deploy events to it.
package updater

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// VersionInfo identifies a build. The running build's values are stamped in at
// compile time via -ldflags; a deploy target's values arrive in the webhook.
type VersionInfo struct {
	Branch     string `json:"branch"`
	CommitHash string `json:"commit_hash"`
	BuildDate  string `json:"build_date"`
}

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
	// LastDeploy is the version the most recent successful deploy switched to.
	// It's what the NEXT process will report as Current after the restart.
	LastDeploy *VersionInfo `json:"last_deploy,omitempty"`
}

type Updater struct {
	mu        sync.RWMutex
	current   VersionInfo
	status    Status
	deploying bool
	// downloadTokenFn returns a bearer token for fetching the binary from a
	// private release asset, or "" for a public download.
	downloadTokenFn func() string
}

// New creates an Updater for the running build. downloadTokenFn may be nil.
func New(branch, commitHash, buildDate string, downloadTokenFn func() string) *Updater {
	if downloadTokenFn == nil {
		downloadTokenFn = func() string { return "" }
	}
	current := VersionInfo{Branch: branch, CommitHash: commitHash, BuildDate: buildDate}
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

// Deploy downloads the binary at downloadURL, replaces the running executable
// with it, and triggers a graceful restart into the new build. target is used
// only for status/logging. It returns once the replacement binary has been
// spawned and this process's shutdown has been signalled; the caller should
// respond to the webhook before this process exits.
//
// Concurrent deploys are rejected: the first one wins and the process is on its
// way out, so a second is meaningless.
func (u *Updater) Deploy(downloadURL string, target VersionInfo) error {
	u.mu.Lock()
	if u.deploying {
		u.mu.Unlock()
		return fmt.Errorf("a deploy is already in progress")
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

	if downloadURL == "" {
		return fail(fmt.Errorf("deploy: empty download_url"))
	}

	log.Printf("Deploy: downloading %s (%s)...", target.DisplayString(), downloadURL)
	tmpFile, err := downloadBinary(downloadURL, u.downloadTokenFn())
	if err != nil {
		return fail(fmt.Errorf("deploy: download binary: %w", err))
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
	u.mu.Unlock()

	log.Printf("Deploy: binary replaced, restarting into %s...", target.DisplayString())
	cmd := exec.Command(execPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("deploy: start new process: %w", err))
	}

	// Signal our own graceful shutdown (SIGTERM) rather than os.Exit(0): main's
	// signal handler seals the secrets keyring, drains in-flight agent runs so
	// they resume in the new process, and drains HTTP — all skipped by a bare
	// exit. The new process's listenWithRetry covers the brief window where
	// both processes hold the port. See main.go.
	go func() {
		time.Sleep(500 * time.Millisecond)
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		} else {
			os.Exit(0)
		}
	}()

	return nil
}

func downloadBinary(url, token string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
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
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "headcount1-deploy-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", err
	}
	tmpFile.Close()

	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}
	return tmpFile.Name(), nil
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

// PlatformAssetName is the release-asset filename for the running OS/arch, e.g.
// "agent-orchestrator-linux-amd64". CI publishes assets under these names and
// the deploy webhook selects the matching one.
func PlatformAssetName() string {
	name := fmt.Sprintf("agent-orchestrator-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}
