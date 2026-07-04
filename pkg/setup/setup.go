package setup

import (
	"bytes"
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"agent-orchestrator/db"
)

//go:embed scripts
var scripts embed.FS

// Failure describes one dependency that the setup script could not install.
// Detail, when present, is the raw command output explaining why — meant to
// be shown behind an expandable disclosure rather than inline.
type Failure struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

var (
	ready          atomic.Bool
	mempalaceReady atomic.Bool
	finished       atomic.Bool
	errStore      atomic.Value // holds string
	warnStore     atomic.Value // holds string
	failuresStore atomic.Value // holds []Failure — blocking failures
	warningsStore atomic.Value // holds []Failure — non-blocking (e.g. gh CLI)
	once          sync.Once
)

// Run executes the platform-appropriate setup script exactly once, blocking
// until it completes. Returns any error produced by the script.
func Run() error {
	once.Do(runOnce)
	if s, _ := errStore.Load().(string); s != "" {
		return fmt.Errorf("%s", s)
	}
	return nil
}

// MarkitdownAvailable reports whether the setup script confirmed markitdown is
// installed and importable.
func MarkitdownAvailable() bool {
	once.Do(runOnce)
	return ready.Load()
}

// MempalaceAvailable reports whether the setup script found/installed the
// mempalace memory engine, independent of other (unrelated) setup failures.
// Blocks until the setup script has run once.
func MempalaceAvailable() bool {
	once.Do(runOnce)
	return mempalaceReady.Load()
}

// MempalaceReadyNonBlocking reports mempalace availability without triggering
// or waiting for the setup script — for request-path checks that must not
// block while setup is still running in the background.
func MempalaceReadyNonBlocking() bool {
	return mempalaceReady.Load()
}

// MempalaceMCPPath returns the path of the mempalace-mcp server binary
// installed by the setup script. On Linux/macOS it lives in the dedicated
// virtualenv (see PythonInterpreter); on Windows it is installed into the
// system python and resolved via PATH.
func MempalaceMCPPath() string {
	if runtime.GOOS == "windows" {
		return "mempalace-mcp"
	}
	return filepath.Join(venvDir(), "bin", "mempalace-mcp")
}

// MempalaceCLIPath returns the path of the mempalace CLI binary (lifecycle
// operations: init, mine, sweep, repair, wake-up).
func MempalaceCLIPath() string {
	if runtime.GOOS == "windows" {
		return "mempalace"
	}
	return filepath.Join(venvDir(), "bin", "mempalace")
}

// StartupError returns a human-readable description of the setup failure, or
// an empty string if setup succeeded.
func StartupError() string {
	once.Do(runOnce)
	s, _ := errStore.Load().(string)
	return s
}

// Status reports setup progress without blocking on the setup script. While
// the script is still running (or hasn't been started yet), pending is true.
// Once it has finished, ok reports whether it succeeded and errMsg describes
// the failure otherwise. warning describes any optional dependencies (e.g.
// gh CLI) that failed to install — these never block the app from starting.
func Status() (pending bool, ok bool, errMsg string, warning string) {
	if !finished.Load() {
		return true, false, "", ""
	}
	s, _ := errStore.Load().(string)
	w, _ := warnStore.Load().(string)
	return false, s == "", s, w
}

// Failures returns the structured list of blocking dependency failures from
// the last setup run, each with a raw-output Detail where one was captured.
func Failures() []Failure {
	f, _ := failuresStore.Load().([]Failure)
	return f
}

// Warnings returns the structured list of non-blocking dependency failures
// (currently just gh CLI) from the last setup run.
func Warnings() []Failure {
	f, _ := warningsStore.Load().([]Failure)
	return f
}

// PythonInterpreter returns the path to the python3 interpreter that has
// markitdown installed and ready to use. On Linux/macOS this is a dedicated
// virtualenv created by the setup script — isolated from the system Python,
// so it never runs into PEP 668's "externally managed environment" guard
// and never risks upgrading a shared dependency out from under some other
// system/Homebrew-managed tool. On Windows, PEP 668 doesn't apply (the
// official installer's Python isn't shared with OS package management), so
// this is just the system python3.
func PythonInterpreter() string {
	if runtime.GOOS == "windows" {
		return "python3"
	}
	return filepath.Join(venvDir(), "bin", "python3")
}

func venvDir() string {
	// PAPERCLIP_VENV_DIR lets deployments (and the e2e harness) point the
	// dependency venv at a persistent location so python installs survive
	// across ephemeral home directories.
	if v := os.Getenv("PAPERCLIP_VENV_DIR"); v != "" {
		return v
	}
	return filepath.Join(db.PaperclipHome(), "venv")
}

func runOnce() {
	defer finished.Store(true)

	scriptData, scriptName, err := selectScript()
	if err != nil {
		store(err.Error())
		return
	}

	tmp, err := os.MkdirTemp("", "paperclip-setup-*")
	if err != nil {
		store(fmt.Sprintf("failed to create temp dir for setup script: %v", err))
		return
	}
	defer os.RemoveAll(tmp)

	scriptPath := filepath.Join(tmp, scriptName)
	if err := os.WriteFile(scriptPath, scriptData, 0o755); err != nil {
		store(fmt.Sprintf("failed to write setup script: %v", err))
		return
	}

	cmd, err := buildCmd(scriptPath)
	if err != nil {
		store(err.Error())
		return
	}
	cmd.Env = append(os.Environ(), "PAPERCLIP_VENV_DIR="+venvDir())

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	log.Println("[setup] Running setup script...")
	runErr := cmd.Run()
	output := out.String()

	// markitdown availability is determined by the script output, independent of
	// whether other dependencies (e.g. github-mcp-server) failed to install.
	if strings.Contains(output, "[setup] markitdown: OK") || strings.Contains(output, "[setup] markitdown: installed") {
		ready.Store(true)
	}

	// mempalace availability follows the same script-output contract. It is a
	// soft dependency: memory features simply stay off when it's absent.
	if strings.Contains(output, "[setup] mempalace: OK") || strings.Contains(output, "[setup] mempalace: installed") {
		mempalaceReady.Store(true)
	}

	if w := extractSoftFailures(output); w != "" {
		log.Printf("[setup] WARNING: %s", w)
		warnStore.Store(w)
	}
	if softFailures := parseSoftFailures(output); len(softFailures) > 0 {
		warningsStore.Store(softFailures)
	}

	if runErr != nil {
		msg := fmt.Sprintf("setup script failed: %v\n%s", runErr, output)
		store(msg)
		if hardFailures := parseFailures(output); len(hardFailures) > 0 {
			failuresStore.Store(hardFailures)
		}
		return
	}

	log.Print(output)
}

// extractSoftFailures collects "[setup] SOFT_FAIL: ..." lines emitted for
// optional dependencies (currently just gh CLI) that failed to install but
// shouldn't block the app from starting.
func extractSoftFailures(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if msg, ok := strings.CutPrefix(line, "[setup] SOFT_FAIL: "); ok {
			lines = append(lines, msg)
		}
	}
	return strings.Join(lines, "; ")
}

var (
	detailBlockRe = regexp.MustCompile(`(?s)\[setup\] DETAIL_BEGIN (\S+)\n(.*?)\n\[setup\] DETAIL_END`)
	bulletRe      = regexp.MustCompile(`(?m)^\s*•\s*([^:]+):\s*(.+)$`)
	softFailRe    = regexp.MustCompile(`(?m)^\[setup\] SOFT_FAIL: (.+?) — (.+)$`)
)

const hardFailureHeader = "[setup] Some dependencies are missing or could not be installed:"

// parseDetails extracts the raw per-dependency command output the setup
// scripts emit between "[setup] DETAIL_BEGIN <id>" / "[setup] DETAIL_END"
// markers, keyed by id (the dependency name with spaces replaced by "_").
func parseDetails(output string) map[string]string {
	details := map[string]string{}
	for _, m := range detailBlockRe.FindAllStringSubmatch(output, -1) {
		details[m[1]] = strings.TrimSpace(m[2])
	}
	return details
}

// parseFailures extracts the structured list of blocking dependency failures
// from the setup script's final summary block, attaching each one's raw
// command output (if captured) from parseDetails.
func parseFailures(output string) []Failure {
	idx := strings.Index(output, hardFailureHeader)
	if idx == -1 {
		return nil
	}
	details := parseDetails(output)
	section := output[idx+len(hardFailureHeader):]
	var failures []Failure
	for _, m := range bulletRe.FindAllStringSubmatch(section, -1) {
		name := strings.TrimSpace(m[1])
		reason := strings.TrimSpace(m[2])
		id := strings.ReplaceAll(name, " ", "_")
		failures = append(failures, Failure{Name: name, Reason: reason, Detail: details[id]})
	}
	return failures
}

// parseSoftFailures extracts the structured list of non-blocking dependency
// failures (currently just gh CLI) from "[setup] SOFT_FAIL: ..." lines.
func parseSoftFailures(output string) []Failure {
	details := parseDetails(output)
	var failures []Failure
	for _, m := range softFailRe.FindAllStringSubmatch(output, -1) {
		name := strings.TrimSpace(m[1])
		reason := strings.TrimSpace(m[2])
		id := strings.ReplaceAll(name, " ", "_")
		failures = append(failures, Failure{Name: name, Reason: reason, Detail: details[id]})
	}
	return failures
}

func store(msg string) {
	log.Printf("[setup] ERROR: %s", msg)
	errStore.Store(msg)
}

func selectScript() (data []byte, name string, err error) {
	switch runtime.GOOS {
	case "linux":
		data, err = scripts.ReadFile("scripts/setup-linux.sh")
		return data, "setup.sh", err
	case "darwin":
		data, err = scripts.ReadFile("scripts/setup-macos.sh")
		return data, "setup.sh", err
	case "windows":
		data, err = scripts.ReadFile("scripts/setup-windows.ps1")
		return data, "setup.ps1", err
	default:
		return nil, "", fmt.Errorf("unsupported OS %q — setup skipped, markdown conversion may be unavailable", runtime.GOOS)
	}
}

func buildCmd(scriptPath string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "linux", "darwin":
		shell, err := exec.LookPath("bash")
		if err != nil {
			shell, err = exec.LookPath("sh")
			if err != nil {
				return nil, fmt.Errorf("no shell found (bash/sh): %w", err)
			}
		}
		return exec.Command(shell, scriptPath), nil
	case "windows":
		ps, err := exec.LookPath("powershell.exe")
		if err != nil {
			ps, err = exec.LookPath("pwsh.exe")
			if err != nil {
				return nil, fmt.Errorf("PowerShell not found (powershell.exe / pwsh.exe): %w", err)
			}
		}
		return exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath), nil
	default:
		return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}
