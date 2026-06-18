package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"
)

// E2E-specific helper functions for the OpenCode engine.
// These functions handle E2E mode setup (killing existing servers, syncing
// config, starting host server) to avoid Docker overhead in tests.

// killExistingOpenCodeServer kills any running opencode server process.
// Used in E2E mode to ensure config changes take effect.
func killExistingOpenCodeServer() {
	_ = exec.Command("pkill", "-f", "opencode serve").Run()
	time.Sleep(500 * time.Millisecond)
}

// startOpenCodeServer starts the opencode server on port 36000 if not running.
// In E2E mode, kills existing server first to apply config changes.
func startOpenCodeServer(e2eMode bool) {
	if e2eMode {
		killExistingOpenCodeServer()
	}

	if _, err := http.Get("http://127.0.0.1:36000/ping"); err != nil {
		fmt.Println("Starting OpenCode server...")
		cmd := exec.Command("opencode", "serve", "--port", "36000")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Set XDG_CONFIG_HOME so opencode reads config from E2E temp dir
		// Set OPENCODE_SERVER_PASSWORD to avoid auth issues with newer versions
		env := os.Environ()
		if e2eHome := os.Getenv("E2E_PAPERCLIP_HOME"); e2eHome != "" {
			env = append(env, "XDG_CONFIG_HOME="+filepath.Join(e2eHome, ".config"))
		}
		password := "e2e-test-password"
		env = append(env, "OPENCODE_SERVER_PASSWORD="+password)
		cmd.Env = env
		fmt.Printf("Starting opencode server with password=%s, E2E_PAPERCLIP_HOME=%s\n", password, os.Getenv("E2E_PAPERCLIP_HOME"))
		if err := cmd.Start(); err != nil {
			fmt.Printf("Failed to start OpenCode server: %v\n", err)
			return
		}
		for i := 0; i < 20; i++ {
			time.Sleep(500 * time.Millisecond)
			// Any response (even 401) means the server is up
			if resp, err := http.Get("http://127.0.0.1:36000/ping"); err == nil {
				resp.Body.Close()
				fmt.Println("OpenCode server is ready.")
				return
			}
		}
		fmt.Println("Warning: OpenCode server didn't become reachable within 10s")
	} else {
		fmt.Println("OpenCode server is already running.")
	}
}

// syncOpenCodeProviderConfig writes ~/.config/opencode/opencode.jsonc with
// provider configurations from the database. Used in E2E mode to configure
// the host OpenCode server to use the mock provider.
func syncOpenCodeProviderConfig(q *db.Queries, runID int32, agentModel string) error {
	configDir := filepath.Join(db.OpencodeConfigDir())
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", configDir, err)
	}
	configPath := filepath.Join(configDir, "opencode.jsonc")

	providers, err := q.ListLLMProviders(context.Background())
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}

	type modelConfig struct {
		Name    string            `json:"name"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	type providerConfig struct {
		NPM     string                `json:"npm"`
		Name    string                `json:"name"`
		Options map[string]string     `json:"options"`
		Models  map[string]modelConfig `json:"models"`
	}
	type configFile struct {
		Schema   string                    `json:"$schema"`
		Provider map[string]providerConfig `json:"provider"`
	}

	cfg := configFile{
		Schema:   "https://opencode.ai/config.json",
		Provider: map[string]providerConfig{},
	}

	for _, p := range providers {
		key := p.ProviderType
		if key == "" {
			key = p.Name
		}
		if key == "" {
			continue
		}

		npm := "@ai-sdk/openai-compatible"
		switch key {
		case "anthropic":
			npm = "@ai-sdk/anthropic"
		}

		// Route LLM traffic through the paperclip2 proxy so we can
		// track per-response activity via TouchRunLastMessageTime.
		proxyURL := os.Getenv("PAPERCLIP_SERVER_URL")
		if proxyURL == "" {
			proxyURL = "http://127.0.0.1:8080"
		}
		baseURL := strings.TrimRight(proxyURL, "/") + "/api/v1"

		modelHeaders := map[string]string{
			"X-Provider-ID": strconv.Itoa(int(p.ID)),
			"X-Run-ID":      strconv.Itoa(int(runID)),
		}
		models := map[string]modelConfig{}
		if p.DefaultModel != "" {
			models[p.DefaultModel] = modelConfig{Name: p.DefaultModel, Headers: modelHeaders}
		}
		if agentModel != "" && agentModel != p.DefaultModel {
			models[agentModel] = modelConfig{Name: agentModel, Headers: modelHeaders}
		}
		cfg.Provider[key] = providerConfig{
			NPM:  npm,
			Name: p.Name,
			Options: map[string]string{
				"baseURL": baseURL,
				"apiKey":  p.ApiKey,
			},
			Models: models,
		}
	}

	configBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, configBytes, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("Synced opencode provider config to %s (%d providers)\n", configPath, len(cfg.Provider))
	return nil
}
