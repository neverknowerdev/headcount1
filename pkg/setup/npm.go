package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// InstallNpmDeps parses depsJSON (a JSON array of npm package names) and
// installs any packages that are not already present globally.
// Returns an error if any package fails to install.
func InstallNpmDeps(ctx context.Context, depsJSON string) error {
	if depsJSON == "" {
		return nil
	}
	var pkgs []string
	if err := json.Unmarshal([]byte(depsJSON), &pkgs); err != nil {
		return fmt.Errorf("invalid deps JSON: %w", err)
	}

	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm not found in PATH — cannot install packages: %w", err)
	}

	var failed []string
	for _, pkg := range pkgs {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		check := exec.CommandContext(ctx, npmPath, "list", "-g", "--depth=0", pkg)
		if check.Run() == nil {
			log.Printf("[setup] npm: %s already installed", pkg)
			continue
		}
		log.Printf("[setup] npm: installing %s...", pkg)
		install := exec.CommandContext(ctx, npmPath, "install", "-g", pkg)
		if out, err := install.CombinedOutput(); err != nil {
			log.Printf("[setup] npm: failed to install %s: %v\n%s", pkg, err, out)
			failed = append(failed, pkg)
		} else {
			log.Printf("[setup] npm: %s installed", pkg)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("npm install failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}
