package deps

import (
	"bytes"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
)

var (
	markitdownReady atomic.Bool
	once            sync.Once
)

// EnsurePythonDeps checks for and installs required Python dependencies at
// startup. It is safe to call from multiple goroutines; the check runs once.
func EnsurePythonDeps() {
	once.Do(ensureOnce)
}

// MarkitdownAvailable reports whether markitdown is importable. EnsurePythonDeps
// must have been called before this is meaningful.
func MarkitdownAvailable() bool {
	once.Do(ensureOnce)
	return markitdownReady.Load()
}

func ensureOnce() {
	if exec.Command("python3", "--version").Run() != nil {
		log.Println("[deps] python3 not found — web_fetch markdown conversion disabled")
		return
	}

	if checkMarkitdown() {
		log.Println("[deps] markitdown OK")
		markitdownReady.Store(true)
		return
	}

	log.Println("[deps] markitdown not found — installing via pip3...")
	var stderr bytes.Buffer
	cmd := exec.Command("pip3", "install", "markitdown")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("[deps] markitdown install failed: %v\n%s", err, stderr.String())
		return
	}

	if checkMarkitdown() {
		log.Println("[deps] markitdown installed successfully")
		markitdownReady.Store(true)
	} else {
		log.Println("[deps] markitdown install reported success but import check failed")
	}
}

func checkMarkitdown() bool {
	return exec.Command("python3", "-c", "from markitdown import MarkItDown").Run() == nil
}
