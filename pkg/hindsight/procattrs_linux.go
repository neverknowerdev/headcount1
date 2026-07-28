package hindsight

import (
	"os/exec"
	"syscall"
)

// setProcAttrs makes the spawned hindsight-api die with the parent process
// (SIGTERM on parent death), so a crashed or killed orchestrator never leaves
// an orphaned memory backend holding the port and the embedded Postgres.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
