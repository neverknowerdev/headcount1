//go:build !linux

package hindsight

import "os/exec"

// setProcAttrs is a no-op outside Linux (no parent-death signal available).
func setProcAttrs(cmd *exec.Cmd) {}
