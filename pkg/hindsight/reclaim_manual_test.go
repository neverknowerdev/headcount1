//go:build reclaim_manual

package hindsight

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Manual verification (requires the real venv binary):
//
//	go test -tags reclaim_manual -run TestReclaimManual ./pkg/hindsight/ -v
//
// Simulates an orphan: spawns hindsight-api detached on a scratch port,
// writes its pid to the pidfile, then calls reclaimOrphans and asserts the
// process is gone.
func TestReclaimManual(t *testing.T) {
	bin := binaryPath()
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("hindsight-api not installed: %v", err)
	}
	cmd := exec.Command(bin, "--port", "9911", "--host", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach like a true orphan
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait() // reap so the kill check sees the process disappear
	t.Logf("spawned fake orphan pid %d", pid)
	time.Sleep(2 * time.Second) // let it boot far enough to be killable

	if !isOurHindsight(pid) {
		t.Fatalf("isOurHindsight(%d) = false for our own spawn", pid)
	}
	if isOurHindsight(os.Getpid()) {
		t.Fatal("isOurHindsight must be false for an unrelated process (the test binary)")
	}

	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}
	reclaimOrphans("9911")

	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("orphan pid %d still alive after reclaimOrphans", pid)
	}
	if _, err := os.Stat(pidFilePath()); !os.IsNotExist(err) {
		t.Fatal("pidfile must be removed after reclamation")
	}
}
