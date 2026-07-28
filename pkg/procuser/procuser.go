// Package procuser runs a child process under a dedicated, unprivileged OS
// account instead of the app's own uid.
//
// Two problems it solves, both documented in doc/hindsight-hardening.md:
//
//  1. Secret containment. hindsight-api receives its API key through its
//     environment. Under a SHARED uid a sandboxed agent shell can read
//     /proc/<pid>/environ and recover it — the exec sandbox restricts paths,
//     and Landlock cannot exclude just one process's /proc entry. A separate
//     uid makes that file unreadable.
//  2. hindsight-api's embedded Postgres (pg0) refuses to run as root
//     ("initdb: error: cannot be run as root"), so any root deployment needs a
//     non-root identity anyway.
//
// The account is never created implicitly — the app should not add users to a
// machine it does not own. Setup is an explicit operator action (see the doc);
// procuser only *uses* a configured account, and says so loudly if it cannot.
package procuser

import "os"

// EnvVar overrides the configured service user for one process.
const EnvVar = "HEADCOUNT1_SERVICE_USER"

// User is a resolved service account. A nil *User means "run as self" and every
// method on it is a no-op, so callers never need a nil check.
type User struct {
	Name string
	UID  int
	GID  int
	Home string
}

// Name of the account, or "" when unset.
func (u *User) String() string {
	if u == nil {
		return ""
	}
	return u.Name
}

// Configured returns the service-user name from the environment, falling back
// to the settings value. Empty means "run as self".
func Configured(fromSettings string) string {
	if v := os.Getenv(EnvVar); v != "" {
		return v
	}
	return fromSettings
}
