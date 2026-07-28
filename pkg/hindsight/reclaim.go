//go:build unix

package hindsight

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agent-orchestrator/db"
)

// Orphan reclamation. On Linux the child dies with us via PDEATHSIG (see
// procattrs_linux.go), but macOS has no parent-death signal: a crashed or
// force-quit orchestrator leaves hindsight-api (and its embedded Postgres)
// running, holding the port and blocking the next startup. Two complementary
// mechanisms find such leftovers before we spawn a fresh process:
//
//   - a pidfile written on every spawn, so the next startup knows exactly
//     which process was ours;
//   - a sweep of whatever still listens on our port, for orphans that predate
//     the pidfile (or whose pidfile was deleted).
//
// A process is only ever killed after its command line has been verified to
// be the venv hindsight-api binary — an unrelated service that happens to
// hold the pid or the port is reported, never touched.

func pidFilePath() string {
	return filepath.Join(db.Headcount1Home(), "hindsight-api.pid")
}

func writePIDFile(pid int) {
	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0644); err != nil {
		log.Printf("hindsight: failed to write pidfile: %v", err)
	}
}

func removePIDFile() {
	_ = os.Remove(pidFilePath())
}

// processArgs returns the full command line of a running process, or "" if
// the process does not exist or cannot be inspected. `ps` is used because it
// works on both macOS and Linux without cgo.
func processArgs(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isOurHindsight reports whether pid is a live process running the app's own
// hindsight-api binary (pids get recycled, so the pidfile alone proves
// nothing).
func isOurHindsight(pid int) bool {
	return strings.Contains(processArgs(pid), binaryPath())
}

// killGracefully SIGTERMs pid and waits for it to exit, escalating to
// SIGKILL after the grace period. The generous wait lets hindsight's
// embedded Postgres shut down cleanly.
func killGracefully(pid int, grace time.Duration) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		// Signal 0 only checks existence.
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// pidsListeningOn returns the pids listening on the TCP port, best-effort
// (empty when lsof is unavailable or nothing listens).
func pidsListeningOn(port string) []int {
	out, err := exec.Command("lsof", "-ti", "tcp:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Fields(string(out)) {
		if pid, perr := strconv.Atoi(line); perr == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// reclaimOrphans finds and stops hindsight-api processes left behind by a
// previous orchestrator that died without cleanup, so the fresh spawn (with
// current LLM config and env) can bind the port. Called from Start before
// spawning.
func reclaimOrphans(port string) {
	// 1. The process recorded by the previous run, wherever it is bound.
	if data, err := os.ReadFile(pidFilePath()); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil {
			if isOurHindsight(pid) {
				log.Printf("hindsight: stopping orphaned hindsight-api from a previous run (pid %d)", pid)
				killGracefully(pid, 15*time.Second)
			}
		}
		removePIDFile()
	}

	// 2. Anything still holding our port (orphans that predate the pidfile).
	for _, pid := range pidsListeningOn(port) {
		if isOurHindsight(pid) {
			log.Printf("hindsight: stopping orphaned hindsight-api holding port %s (pid %d)", port, pid)
			killGracefully(pid, 15*time.Second)
		} else if args := processArgs(pid); args != "" {
			log.Printf("hindsight: port %s is held by an unrelated process (pid %d: %.80s) — not touching it, startup will likely fail", port, pid, args)
		}
	}
}
