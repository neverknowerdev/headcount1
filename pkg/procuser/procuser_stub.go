//go:build !unix

package procuser

import (
	"fmt"
	"os"
	"os/exec"
)

// Resolve rejects a configured service user outside POSIX: syscall.Credential
// does not exist there, so honouring the setting is impossible and pretending
// to would be worse than refusing. An empty name (the default) is fine.
func Resolve(name string) (*User, error) {
	if name == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("service user %q: running children under another account is not supported on this platform", name)
}

// Apply is a no-op; Resolve never returns a non-nil User here.
func (u *User) Apply(cmd *exec.Cmd) {}

// EnsureOwned only creates the directory; there is no uid to hand it to.
func (u *User) EnsureOwned(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}
