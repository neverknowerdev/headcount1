//go:build unix

package procuser

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// Resolve looks up name once at startup. An empty name yields (nil, nil):
// "run as self", the default. A configured-but-missing account is a hard
// error — silently ignoring it would leave the operator believing the process
// is isolated when it is not.
func Resolve(name string) (*User, error) {
	if name == "" {
		return nil, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("service user %q: %w (create it first — see doc/hindsight-hardening.md)", name, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("service user %q: unusable uid %q: %w", name, u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("service user %q: unusable gid %q: %w", name, u.Gid, err)
	}
	return &User{Name: name, UID: uid, GID: gid, Home: u.HomeDir}, nil
}

var warnOnce sync.Once

// Apply makes cmd run as u. It preserves any SysProcAttr the caller already
// set (e.g. Pdeathsig) and rewrites HOME/USER/LOGNAME so the child's caches
// and state land under the service account rather than in an unwritable home.
//
// Switching uid requires the parent to be root (or hold CAP_SETUID). When it
// is not, Apply logs once and leaves cmd alone: a dev machine must still run.
func (u *User) Apply(cmd *exec.Cmd) {
	if u == nil || os.Getuid() == u.UID {
		return
	}
	if os.Getuid() != 0 {
		warnOnce.Do(func() {
			log.Printf("procuser: service user %q configured but this process is not root — running children as the current user instead", u.Name)
		})
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(u.UID), Gid: uint32(u.GID)}

	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(stripEnv(env, "HOME", "USER", "LOGNAME"),
		"HOME="+u.Home, "USER="+u.Name, "LOGNAME="+u.Name)
}

func stripEnv(env []string, keys ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		drop := false
		for _, k := range keys {
			if len(kv) > len(k) && kv[:len(k)] == k && kv[len(k)] == '=' {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// EnsureOwned creates path if missing and hands it to the service account, so
// directories both sides touch (memory exports, the venv's state) stay usable
// after the uid switch. Only meaningful as root; otherwise it just ensures the
// directory exists.
func (u *User) EnsureOwned(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	if u == nil || os.Getuid() != 0 {
		return nil
	}
	// Ownership must reach the leaf's contents too — exports written earlier as
	// root would otherwise be unreadable to the child.
	return filepath.WalkDir(path, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, u.UID, u.GID)
	})
}
