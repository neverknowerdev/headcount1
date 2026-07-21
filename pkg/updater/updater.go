package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	githubAPIBase = "https://api.github.com"
	repoOwner     = "neverknowerdev"
	repoName      = "paperclip2"
)

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

type UpdateStatus struct {
	Current         VersionInfo  `json:"current"`
	Latest          *VersionInfo `json:"latest,omitempty"`
	UpdateAvailable bool         `json:"update_available"`
	LastChecked     *time.Time   `json:"last_checked,omitempty"`
	Checking        bool         `json:"checking"`
	Error           string       `json:"error,omitempty"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type releaseMetadata struct {
	CommitHash string `json:"commit_hash"`
	BuildDate  string `json:"build_date"`
	Branch     string `json:"branch"`
}

const DefaultCheckInterval = 60 * time.Minute

type Updater struct {
	mu            sync.RWMutex
	current       VersionInfo
	trackedBranch string
	checkInterval time.Duration
	status        UpdateStatus
	githubPATFn   func() string
	autoApplyFn   func() bool
	stopCh        chan struct{}
}

func New(branch, commitHash, buildDate string, githubPATFn func() string) *Updater {
	current := VersionInfo{
		Branch:     branch,
		CommitHash: commitHash,
		BuildDate:  buildDate,
	}
	if branch == "" {
		branch = "main"
	}
	return &Updater{
		current:       current,
		trackedBranch: branch,
		checkInterval: DefaultCheckInterval,
		status:        UpdateStatus{Current: current},
		githubPATFn:   githubPATFn,
		stopCh:        make(chan struct{}),
	}
}

// SetAutoApplyFn installs a predicate consulted after each periodic check. When
// it returns true and an update is available, the periodic loop downloads and
// applies the new binary automatically (equivalent to the user clicking "Apply").
func (u *Updater) SetAutoApplyFn(fn func() bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.autoApplyFn = fn
}

func (u *Updater) SetCheckInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checkInterval = d
}

func (u *Updater) GetCheckIntervalMins() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return int(u.checkInterval.Minutes())
}

func (u *Updater) GetStatus() UpdateStatus {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status
}

func (u *Updater) SetTrackedBranch(branch string) {
	if branch == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.trackedBranch = branch
}

func (u *Updater) GetTrackedBranch() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.trackedBranch
}

func (u *Updater) CheckForUpdate() error {
	u.mu.Lock()
	u.status.Checking = true
	branch := u.trackedBranch
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.status.Checking = false
		u.mu.Unlock()
	}()

	release, err := u.fetchRelease(branch)
	if err != nil {
		now := time.Now()
		u.mu.Lock()
		u.status.Error = err.Error()
		u.status.LastChecked = &now
		u.mu.Unlock()
		return err
	}

	var meta releaseMetadata
	if err := json.Unmarshal([]byte(release.Body), &meta); err != nil {
		errMsg := fmt.Sprintf("failed to parse release metadata: %v", err)
		u.mu.Lock()
		u.status.Error = errMsg
		u.mu.Unlock()
		return fmt.Errorf("%s", errMsg)
	}

	latest := &VersionInfo{
		Branch:     meta.Branch,
		CommitHash: meta.CommitHash,
		BuildDate:  meta.BuildDate,
	}

	now := time.Now()
	u.mu.Lock()
	u.status.Latest = latest
	u.status.LastChecked = &now
	u.status.UpdateAvailable = meta.CommitHash != "" && meta.CommitHash != u.current.CommitHash
	u.status.Error = ""
	u.mu.Unlock()

	return nil
}

func (u *Updater) ApplyUpdate() error {
	u.mu.RLock()
	branch := u.trackedBranch
	u.mu.RUnlock()

	release, err := u.fetchRelease(branch)
	if err != nil {
		return fmt.Errorf("fetch release: %w", err)
	}

	assetName := platformAssetName()
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no binary for platform %s in release %q", assetName, branch)
	}

	tmpFile, err := downloadBinary(downloadURL, u.githubPATFn())
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("eval symlinks: %w", err)
	}

	if err := os.Rename(tmpFile, execPath); err != nil {
		// Rename may fail cross-device; fall back to copy
		if copyErr := copyFile(tmpFile, execPath); copyErr != nil {
			os.Remove(tmpFile)
			return fmt.Errorf("replace binary: %w (copy also failed: %v)", err, copyErr)
		}
		os.Remove(tmpFile)
	}

	log.Printf("Update applied, restarting with new binary...")
	cmd := exec.Command(execPath, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start new process: %w", err)
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()

	return nil
}

// StartPeriodicCheck starts a background goroutine that checks for updates.
// It re-reads the interval before each wait, so changes via SetCheckInterval
// take effect after the current sleep completes.
func (u *Updater) StartPeriodicCheck() {
	go func() {
		for {
			u.mu.RLock()
			interval := u.checkInterval
			u.mu.RUnlock()

			select {
			case <-time.After(interval):
				if err := u.CheckForUpdate(); err != nil {
					log.Printf("Periodic update check failed: %v", err)
					continue
				}
				u.mu.RLock()
				available := u.status.UpdateAvailable
				autoApply := u.autoApplyFn
				u.mu.RUnlock()
				if available && autoApply != nil && autoApply() {
					log.Printf("Auto-update enabled and new version available — applying...")
					if err := u.ApplyUpdate(); err != nil {
						log.Printf("Auto-update failed: %v", err)
					}
				}
			case <-u.stopCh:
				return
			}
		}
	}()
}

func (u *Updater) Stop() {
	select {
	case <-u.stopCh:
	default:
		close(u.stopCh)
	}
}

// branchToTag converts a branch name to a valid git tag (replaces / with -).
// The CI workflow applies the same transformation when creating releases.
func branchToTag(branch string) string {
	return strings.ReplaceAll(branch, "/", "-")
}

func (u *Updater) fetchRelease(branch string) (*githubRelease, error) {
	tag := branchToTag(branch)
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", githubAPIBase, repoOwner, repoName, tag)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "paperclip2-updater")
	if pat := u.githubPATFn(); pat != "" {
		req.Header.Set("Authorization", "token "+pat)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no release found for branch %q (tag %q) — make sure CI has published a release", branch, tag)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d for release tag %q", resp.StatusCode, tag)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

func downloadBinary(url, pat string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "paperclip2-updater")
	if pat != "" {
		req.Header.Set("Authorization", "token "+pat)
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

	tmpFile, err := os.CreateTemp("", "paperclip2-update-*")
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

func platformAssetName() string {
	name := fmt.Sprintf("agent-orchestrator-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}
