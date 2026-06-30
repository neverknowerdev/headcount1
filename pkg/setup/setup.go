package setup

import (
	"bytes"
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed scripts
var scripts embed.FS

var (
	ready    atomic.Bool
	errStore atomic.Value // holds string
	once     sync.Once
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

// StartupError returns a human-readable description of the setup failure, or
// an empty string if setup succeeded.
func StartupError() string {
	once.Do(runOnce)
	s, _ := errStore.Load().(string)
	return s
}

func runOnce() {
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

	if runErr != nil {
		msg := fmt.Sprintf("setup script failed: %v\n%s", runErr, output)
		store(msg)
		return
	}

	log.Print(output)
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
