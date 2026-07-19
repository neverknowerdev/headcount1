//go:build linux

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// childConfig is the JSON payload handed to a hardened sandbox re-exec child
// (argv[4]). It carries the paths the child must grant, computed in the parent
// as the server uid — the child may run as a different uid whose home dir and
// caches differ, so it cannot recompute these itself.
type childConfig struct {
	WritableDirs []string `json:"w"`
	ReadOnlyDirs []string `json:"r"`
	ReadScoping  bool     `json:"s"`
}

// readScopeRoots are the system directories a toolchain needs to read. It
// deliberately OMITS the user's home directory, so an agent under read-scoping
// cannot reach ~/.headcount1 (DB, keystore, keyring snapshot, SSH keys). The
// workspace, toolchain caches, and read-only roots are granted separately.
// IgnoreIfMissing tolerates absent entries across distros.
//
// It also deliberately omits the wholesale /run and /var trees, granting only
// safe subdirectories: those trees hold container secret mounts that are
// world-readable (0444) and therefore reachable EVEN WITH a dedicated uid —
// Docker secrets at /run/secrets/*, Kubernetes service-account tokens at
// /var/run/secrets/kubernetes.io/serviceaccount/token, systemd credentials
// under /run. Not granting the parent trees keeps them out of reach.
//
// Note: /proc is granted because toolchains read /proc/self, /proc/cpuinfo,
// etc., but Landlock cannot exclude just /proc/<other-pid>. Under a SHARED uid
// (read-scoping without a dedicated uid) the agent can still read
// /proc/<server_pid>/environ; only a dedicated uid closes that. Read-scoping
// alone is not a substitute for the dedicated uid — see doc/sandbox-hardening.md.
var readScopeRoots = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32", "/libx32",
	"/etc", "/opt", "/proc", "/sys", "/dev", "/snap",
	"/var/tmp", "/var/cache", "/var/lib",
}

// sandboxChildArg is the hidden argv[1] marker that tells a re-executed copy
// of this binary to apply the Landlock ruleset and exec the shell command.
const sandboxChildArg = "__headcount1-sandbox-child__"

// landlockABI returns the kernel's Landlock ABI version (0 if unsupported).
func landlockABI() int {
	v, err := llsys.LandlockGetABIVersion()
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func sandboxDescription() string {
	v := landlockABI()
	if v == 0 {
		return "DISABLED — kernel lacks Landlock support; only path validation applies"
	}
	desc := fmt.Sprintf("Landlock (kernel ABI v%d) — writes restricted to the workspace", v)
	if h := loadSandboxHardening(); h.active() {
		extra := []string{}
		if h.uid > 0 {
			extra = append(extra, fmt.Sprintf("dedicated uid %d", h.uid))
		}
		if h.readScoping {
			extra = append(extra, "read-scoped (home hidden)")
		}
		desc += " [+" + strings.Join(extra, ", +") + "]"
	}
	return desc
}

// sandboxedCommand builds the command that runs `sh -c command` under a
// Landlock ruleset. Landlock rules apply to the calling process and are
// inherited by children, and Go cannot run code between fork and exec — so we
// re-execute our own binary, which applies the ruleset to itself (see
// MaybeRunSandboxChild) and then execs the shell in place.
func sandboxedCommand(ctx context.Context, workspacePath, command string, readOnlyDirs []string) (*exec.Cmd, func(), error) {
	h := loadSandboxHardening()

	// Resolve the dedicated sandbox uid's home once — needed both for the
	// writable cache grants and for the child's HOME/USER env.
	var sandboxHome, sandboxUser string
	if h.uid > 0 {
		if u, err := user.LookupId(strconv.Itoa(h.uid)); err == nil {
			sandboxHome, sandboxUser = u.HomeDir, u.Username
		}
	}

	if landlockABI() == 0 {
		// No Landlock write sandbox on this kernel. Historically this meant
		// "run unsandboxed", but the dedicated-uid drop is INDEPENDENT of
		// Landlock and is the primary isolation of the server's secret files
		// and /proc from the untrusted shell — so it must still be applied when
		// configured. A configured uid that can't be dropped to fails closed at
		// cmd.Start() (missing CAP_SETUID) rather than silently running the
		// untrusted shell at the server's (possibly root) uid.
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		if h.uid > 0 {
			applySandboxCredential(cmd, h)
			cmd.Env = sandboxEnvForHome(sandboxHome, sandboxUser)
		}
		return cmd, nil, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot locate own executable for sandbox re-exec: %w", err)
	}
	args := []string{sandboxChildArg, workspacePath, command}
	if h.active() {
		// Compute the grant lists here (as the server uid) and hand them to the
		// child. When a dedicated sandbox uid is used, the toolchain caches must
		// resolve against THAT uid's home — the server's ~/.cache etc. are owned
		// by the server uid and unwritable by the sandbox uid, so `go build` /
		// `npm install` would fail without this.
		writable := extraWritableDirs()
		if h.uid > 0 && sandboxHome != "" {
			writable = extraWritableDirsForHome(sandboxHome)
		}
		cfg := childConfig{
			WritableDirs: writable,
			ReadOnlyDirs: readOnlyDirs,
			ReadScoping:  h.readScoping,
		}
		if blob, err := json.Marshal(cfg); err == nil {
			args = append(args, base64.StdEncoding.EncodeToString(blob))
		}
	}
	cmd := exec.CommandContext(ctx, self, args...)
	if h.uid > 0 {
		applySandboxCredential(cmd, h)
		cmd.Env = sandboxEnvForHome(sandboxHome, sandboxUser)
	}
	return cmd, nil, nil
}

// applySandboxCredential drops the child to the dedicated unprivileged
// uid/gid. Groups is set to exactly the primary gid with NoSetGroups=false so
// the kernel calls setgroups() and CLEARS the server's supplementary groups;
// leaving NoSetGroups=true would SKIP setgroups and let the sandbox uid inherit
// whatever groups the server process belongs to (e.g. a shared data/docker
// group), undermining the isolation the dedicated uid provides. Requires
// CAP_SETUID; without it cmd.Start() fails loudly rather than running
// privileged.
func applySandboxCredential(cmd *exec.Cmd, h sandboxHardening) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:         uint32(h.uid),
			Gid:         uint32(h.gid),
			Groups:      []uint32{uint32(h.gid)},
			NoSetGroups: false,
		},
	}
}

// MaybeRunSandboxChild must be called first thing in main() (and in TestMain
// of any test binary that executes the bash tool). When the process was
// spawned as a sandbox re-exec child it never returns: it applies the
// Landlock ruleset and replaces itself with `sh -c <command>` via execve, so
// the shell keeps this process's PID and the parent's timeout/kill still
// applies. In a normal process start it is a no-op.
func MaybeRunSandboxChild() {
	if len(os.Args) < 4 || os.Args[1] != sandboxChildArg {
		return
	}
	workspace, command := os.Args[2], os.Args[3]
	// Legacy (4-arg) invocation: no extra hardening, read everything, writable
	// dirs computed locally. Hardened (5-arg) invocation carries the grant
	// lists the parent computed as the server uid.
	cfg := childConfig{WritableDirs: extraWritableDirs()}
	if len(os.Args) >= 5 {
		if blob, err := base64.StdEncoding.DecodeString(os.Args[4]); err == nil {
			_ = json.Unmarshal(blob, &cfg)
		}
	}
	if err := restrictWritesToWorkspace(workspace, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: applying landlock rules: %v\n", err)
		os.Exit(125)
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox: sh not found in PATH")
		os.Exit(127)
	}
	if err := syscall.Exec(shell, []string{"sh", "-c", command}, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: exec sh: %v\n", err)
		os.Exit(126)
	}
}

// restrictWritesToWorkspace applies a Landlock ruleset to the current process:
// write access only inside the workspace, temp dirs, and toolchain caches, plus
// read access. Read access is normally unrestricted (toolchains live all over
// the filesystem); under read-scoping it becomes an explicit allowlist of
// system roots that excludes the server's home, so ~/.headcount1 secrets stay
// unreadable. BestEffort degrades gracefully on kernels with older Landlock
// ABIs (the parent already verified ABI >= 1 before re-executing).
func restrictWritesToWorkspace(workspace string, cfg childConfig) error {
	rw := append([]string{workspace}, cfg.WritableDirs...)
	rules := []landlock.Rule{
		// WithRefer allows mv/ln across directories inside the writable trees
		// (a separate Landlock access right since ABI v2).
		landlock.RWDirs(rw...).WithRefer().IgnoreIfMissing(),
		landlock.RWFiles(
			"/dev/null", "/dev/zero", "/dev/full",
			"/dev/tty", "/dev/ptmx",
			"/dev/random", "/dev/urandom",
		).WithIoctlDev().IgnoreIfMissing(),
	}
	if cfg.ReadScoping {
		// Curated system roots (no home dir) + the explicit read-only roots the
		// tools were configured with. The workspace and caches are already
		// readable via the RW grants above.
		roots := append(append([]string{}, readScopeRoots...), cfg.ReadOnlyDirs...)
		rules = append(rules, landlock.RODirs(roots...).IgnoreIfMissing())
	} else {
		rules = append(rules, landlock.RODirs("/"))
	}
	return landlock.V5.BestEffort().RestrictPaths(rules...)
}
