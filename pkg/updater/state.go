package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// successfulReleaseRetention includes the current binary, its latest
// successful fallback, and ten older successful binaries. The currently
// running binary may live outside this directory on an older installation, so
// it is never counted here.
const successfulReleaseRetention = 12

type DeployPhase string

const (
	DeployPhaseStaged              DeployPhase = "staged"
	DeployPhaseMigrating           DeployPhase = "migrating"
	DeployPhaseStarting            DeployPhase = "starting"
	DeployPhasePromoted            DeployPhase = "promoted"
	DeployPhaseFailed              DeployPhase = "failed"
	DeployPhaseNeedsManualRecovery DeployPhase = "needs_manual_recovery"
)

type DeploymentState struct {
	ID                 string          `json:"id"`
	Phase              DeployPhase     `json:"phase"`
	Target             VersionInfo     `json:"target"`
	CandidatePath      string          `json:"candidate_path"`
	PreviousPath       string          `json:"previous_path"`
	ArtifactSHA256     string          `json:"artifact_sha256"`
	DatabaseSnapshot   string          `json:"database_snapshot,omitempty"`
	DatabaseSchema     string          `json:"database_schema,omitempty"`
	StartedAt          time.Time       `json:"started_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	LastError          string          `json:"last_error,omitempty"`
	SuccessfulReleases []ReleaseRecord `json:"successful_releases,omitempty"`
}

type ReleaseRecord struct {
	ID             string      `json:"id"`
	Target         VersionInfo `json:"target"`
	Path           string      `json:"path"`
	ArtifactSHA256 string      `json:"artifact_sha256"`
	PromotedAt     time.Time   `json:"promoted_at"`
}

func statePath(basePath string) string {
	if basePath == "" {
		return ""
	}
	return filepath.Join(basePath, "deploy", "state.json")
}

func LoadState(basePath string) (DeploymentState, error) {
	path := statePath(basePath)
	if path == "" {
		return DeploymentState{}, errors.New("deployment state path is not configured")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return DeploymentState{}, err
	}
	var state DeploymentState
	if err := json.Unmarshal(b, &state); err != nil {
		return DeploymentState{}, fmt.Errorf("decode deployment state: %w", err)
	}
	return state, nil
}

func SaveState(basePath string, state DeploymentState) error {
	path := statePath(basePath)
	if path == "" {
		return errors.New("deployment state path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deploy-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s DeploymentState) Status(current VersionInfo) Status {
	status := Status{Current: current, Phase: string(s.Phase), DeploymentID: s.ID}
	if s.Target.CommitHash != "" {
		target := s.Target
		status.LastDeploy = &target
	}
	status.LastError = s.LastError
	status.Deploying = s.Phase == DeployPhaseStaged || s.Phase == DeployPhaseMigrating || s.Phase == DeployPhaseStarting
	return status
}

func appendSuccessfulRelease(state *DeploymentState, record ReleaseRecord) {
	if record.ID == "" || record.Path == "" {
		return
	}
	for i := range state.SuccessfulReleases {
		if state.SuccessfulReleases[i].ID == record.ID {
			state.SuccessfulReleases[i] = record
			if len(state.SuccessfulReleases) > successfulReleaseRetention {
				state.SuccessfulReleases = append([]ReleaseRecord(nil), state.SuccessfulReleases[len(state.SuccessfulReleases)-successfulReleaseRetention:]...)
			}
			return
		}
	}
	state.SuccessfulReleases = append(state.SuccessfulReleases, record)
	if len(state.SuccessfulReleases) > successfulReleaseRetention {
		state.SuccessfulReleases = append([]ReleaseRecord(nil), state.SuccessfulReleases[len(state.SuccessfulReleases)-successfulReleaseRetention:]...)
	}
}

func releaseIDForPath(root, candidatePath string) (string, bool) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidatePath, err = filepath.Abs(candidatePath)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, candidatePath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 2 || parts[1] != "agent-orchestrator" {
		return "", false
	}
	return parts[0], true
}

// pruneReleases removes only candidate directories created by this updater.
// It deliberately keeps the previous path while a deployment is in flight or
// failed, because that binary is the fallback currently being served.
func pruneReleases(basePath string, state DeploymentState) error {
	root := filepath.Join(basePath, "releases")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	keep := make(map[string]struct{}, successfulReleaseRetention+2)
	for _, release := range state.SuccessfulReleases {
		if id, ok := releaseIDForPath(root, release.Path); ok {
			keep[id] = struct{}{}
		}
	}
	if state.Phase == DeployPhaseStaged || state.Phase == DeployPhaseMigrating || state.Phase == DeployPhaseStarting || state.Phase == DeployPhasePromoted {
		if id, ok := releaseIDForPath(root, state.CandidatePath); ok {
			keep[id] = struct{}{}
		}
	}
	if id, ok := releaseIDForPath(root, state.PreviousPath); ok {
		keep[id] = struct{}{}
	}

	var removeErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), "agent-orchestrator")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if _, ok := keep[entry.Name()]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil && removeErr == nil {
			removeErr = err
		}
	}
	return removeErr
}

func (u *Updater) activeCandidate(state DeploymentState) bool {
	return state.CandidatePath != "" && state.Target.CommitHash == u.current.CommitHash &&
		(state.Phase == DeployPhaseStaged || state.Phase == DeployPhaseMigrating || state.Phase == DeployPhaseStarting)
}

func deploymentID(target VersionInfo, digest string) string {
	seed := target.CommitHash + "\x00" + digest
	sum := sha256.Sum256([]byte(seed))
	commit := strings.TrimSpace(target.CommitHash)
	if commit == "" {
		commit = "unknown"
	}
	return commit + "-" + hex.EncodeToString(sum[:])[:12]
}

// MarkMigrating records the phase before a candidate touches the database.
func (u *Updater) MarkMigrating() error {
	if u.basePath == "" {
		return nil
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !u.activeCandidate(state) {
		return nil
	}
	state.Phase = DeployPhaseMigrating
	return SaveState(u.basePath, state)
}

// MarkPromoted clears the pending deployment after the candidate completed
// migrations and reached the normal application startup path.
func (u *Updater) MarkPromoted() error {
	if u.basePath == "" {
		return nil
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !u.activeCandidate(state) {
		return nil
	}
	state.Phase = DeployPhasePromoted
	state.LastError = ""
	appendSuccessfulRelease(&state, ReleaseRecord{
		ID: state.ID, Target: state.Target, Path: state.CandidatePath,
		ArtifactSHA256: state.ArtifactSHA256, PromotedAt: time.Now().UTC(),
	})
	if err := SaveState(u.basePath, state); err != nil {
		return err
	}
	if err := pruneReleases(u.basePath, state); err != nil {
		log.Printf("Warning: could not prune old deploy binaries: %v", err)
	}
	u.mu.Lock()
	u.status.Deploying = false
	u.status.Phase = string(DeployPhasePromoted)
	u.status.LastError = ""
	u.mu.Unlock()
	return nil
}

// MarkStarting records that migrations completed and the candidate is now
// binding its serving socket. Promotion is delayed until the listener is
// actually bound, so CI can distinguish a boot that is still starting from a
// build that is serving traffic.
func (u *Updater) MarkStarting() error {
	if u.basePath == "" {
		return nil
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !u.activeCandidate(state) {
		return nil
	}
	state.Phase = DeployPhaseStarting
	if err := SaveState(u.basePath, state); err != nil {
		return err
	}
	u.mu.Lock()
	u.status.Deploying = true
	u.status.Phase = string(DeployPhaseStarting)
	u.mu.Unlock()
	return nil
}

// AttachSQLiteSnapshot records the checkpoint created after the old child has
// closed its database and before a candidate is started.
func (u *Updater) AttachSQLiteSnapshot(snapshot SQLiteSnapshot) error {
	if u.basePath == "" || snapshot.Path == "" {
		return nil
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		return err
	}
	state.DatabaseSnapshot = snapshot.Path
	return SaveState(u.basePath, state)
}

// RecordStartupFailure persists a candidate boot failure and returns the old
// executable path when this process should exec back into the last-known-good
// binary. It intentionally does not perform database rollback; the caller must
// reconcile migrations first, then call this method.
func (u *Updater) RecordStartupFailure(current VersionInfo, cause error) (string, error) {
	if u.basePath == "" {
		return "", nil
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if state.Target.CommitHash != current.CommitHash || state.CandidatePath == "" {
		return "", nil
	}
	state.Phase = DeployPhaseFailed
	state.LastError = cause.Error()
	if err := SaveState(u.basePath, state); err != nil {
		return "", err
	}
	u.mu.Lock()
	u.status = state.Status(current)
	u.status.Deploying = false
	u.mu.Unlock()
	return state.PreviousPath, nil
}

// MarkNeedsManualRecovery preserves the failure detail when automatic schema
// rollback itself failed. The previous binary can still be started, but the
// database must not be treated as safe for another automatic attempt until an
// operator repairs or verifies it.
func (u *Updater) MarkNeedsManualRecovery(current VersionInfo, cause error) error {
	if u.basePath == "" {
		return nil
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.Target.CommitHash != current.CommitHash || state.CandidatePath == "" {
		return nil
	}
	state.Phase = DeployPhaseNeedsManualRecovery
	state.LastError = cause.Error()
	if err := SaveState(u.basePath, state); err != nil {
		return err
	}
	u.mu.Lock()
	u.status = state.Status(current)
	u.status.Deploying = false
	u.mu.Unlock()
	return nil
}

func (u *Updater) DeploymentState(id string) (DeploymentState, bool, error) {
	if u.basePath == "" {
		return DeploymentState{}, false, errors.New("deployment state path is not configured")
	}
	state, err := LoadState(u.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return DeploymentState{}, false, nil
		}
		return DeploymentState{}, false, err
	}
	if id != "" && state.ID != id {
		return DeploymentState{}, false, nil
	}
	return state, true, nil
}
