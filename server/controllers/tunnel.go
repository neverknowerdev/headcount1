package endpoints

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type TunnelStatusResponse struct {
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Status   string `json:"status"` // "stopped", "starting", "running", "error"
	Error    string `json:"error,omitempty"`
}

var (
	tunnelMu     sync.Mutex
	tunnelProc   *os.Process
	tunnelURL    string
	tunnelSt     = "stopped"
	tunnelErr    string
	tunnelCancel context.CancelFunc
)

// InitTunnel restores tunnel state on server startup if previously enabled.
func InitTunnel() {
	settings := LoadSettings()
	if settings.TunnelEnabled && settings.TunnelProvider != "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		tunnelMu.Lock()
		tunnelSt = "starting"
		tunnelErr = ""
		tunnelMu.Unlock()
		go launchTunnel(settings.TunnelProvider, port)
	}
}

func (api *API) GetTunnelStatus(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()

	tunnelMu.Lock()
	resp := TunnelStatusResponse{
		Provider: settings.TunnelProvider,
		Enabled:  tunnelSt == "running",
		URL:      tunnelURL,
		Status:   tunnelSt,
		Error:    tunnelErr,
	}
	tunnelMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (api *API) StartTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Provider != "tailscale" && req.Provider != "cloudflare" {
		http.Error(w, "provider must be 'tailscale' or 'cloudflare'", http.StatusBadRequest)
		return
	}

	haltTunnel()

	settings := LoadSettings()
	settings.TunnelProvider = req.Provider
	settings.TunnelEnabled = true
	if err := SaveSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	tunnelMu.Lock()
	tunnelSt = "starting"
	tunnelErr = ""
	tunnelMu.Unlock()

	go launchTunnel(req.Provider, port)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "starting"})
}

func (api *API) StopTunnel(w http.ResponseWriter, r *http.Request) {
	settings := LoadSettings()
	provider := settings.TunnelProvider

	haltTunnel()

	if provider == "tailscale" {
		if out, err := exec.Command("tailscale", "funnel", "reset").CombinedOutput(); err != nil {
			log.Printf("[tunnel] tailscale funnel reset: %s %v", out, err)
		}
	}

	settings.TunnelEnabled = false
	if err := SaveSettings(settings); err != nil {
		log.Printf("[tunnel] failed to save settings: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

func haltTunnel() {
	tunnelMu.Lock()
	defer tunnelMu.Unlock()

	if tunnelCancel != nil {
		tunnelCancel()
		tunnelCancel = nil
	}
	if tunnelProc != nil {
		tunnelProc.Kill()
		tunnelProc = nil
	}
	tunnelURL = ""
	tunnelSt = "stopped"
	tunnelErr = ""
}

func launchTunnel(provider, port string) {
	switch provider {
	case "cloudflare":
		launchCloudflareTunnel(port)
	case "tailscale":
		launchTailscaleFunnel(port)
	default:
		setTunnelError("unknown provider: " + provider)
	}
}

var cfURLRegex = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// launchCloudflareTunnel runs cloudflared in a restart loop with exponential
// backoff. It exits only when the context is cancelled (user stops the tunnel)
// or the binary is missing (fatal, no point retrying).
func launchCloudflareTunnel(port string) {
	ctx, cancel := context.WithCancel(context.Background())
	tunnelMu.Lock()
	tunnelCancel = cancel
	tunnelMu.Unlock()

	const maxBackoff = 32 * time.Second
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		crashed, fatal := runOneCloudflareTunnel(ctx, port)

		if ctx.Err() != nil {
			return // intentional stop by user
		}
		if fatal {
			return // binary missing or other unrecoverable error
		}

		if crashed {
			// Was running fine, then died — reset backoff since it worked before
			backoff = time.Second
		}

		log.Printf("[tunnel] cloudflared not running, restarting in %s", backoff)
		tunnelMu.Lock()
		tunnelURL = ""
		tunnelSt = "starting"
		tunnelErr = ""
		tunnelMu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// runOneCloudflareTunnel starts one cloudflared process and blocks until it
// exits. Returns (crashed, fatal):
//   - crashed=true  — process ran successfully (URL obtained), then died
//   - fatal=true    — unrecoverable error (binary not found); caller should not retry
func runOneCloudflareTunnel(ctx context.Context, port string) (crashed bool, fatal bool) {
	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "--url",
		fmt.Sprintf("http://localhost:%s", port), "--no-autoupdate")

	stderr, err := cmd.StderrPipe()
	if err != nil {
		setTunnelError("failed to create stderr pipe: " + err.Error())
		return false, false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		setTunnelError("failed to create stdout pipe: " + err.Error())
		return false, false
	}

	if err := cmd.Start(); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file") {
			setTunnelError("cloudflared not found — please install it first (see instructions above).")
			return false, true
		}
		setTunnelError("failed to start cloudflared: " + msg)
		return false, false
	}

	tunnelMu.Lock()
	tunnelProc = cmd.Process
	tunnelMu.Unlock()

	urlFound := make(chan string, 1)
	done := make(chan error, 1)

	scan := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[cloudflared] %s", line)
			if match := cfURLRegex.FindString(line); match != "" {
				select {
				case urlFound <- match:
				default:
				}
			}
		}
	}
	go scan(stderr)
	go scan(stdout)
	go func() { done <- cmd.Wait() }()

	// Wait for URL, early exit, or context cancellation
	select {
	case url := <-urlFound:
		tunnelMu.Lock()
		tunnelURL = url
		tunnelSt = "running"
		tunnelMu.Unlock()
		log.Printf("[tunnel] Cloudflare URL ready: %s", url)
	case <-done:
		return false, false // exited before URL appeared
	case <-ctx.Done():
		<-done
		return false, false
	}

	// Process is up and serving — wait for it to die
	select {
	case <-done:
		if ctx.Err() != nil {
			return false, false
		}
		log.Printf("[tunnel] cloudflared crashed")
		return true, false
	case <-ctx.Done():
		<-done
		return false, false
	}
}

func launchTailscaleFunnel(port string) {
	out, err := exec.Command("tailscale", "funnel", port).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file") {
			msg = "tailscale not found — please install Tailscale first (see instructions above)."
		} else if msg == "" {
			msg = "tailscale funnel failed: " + err.Error()
		}
		setTunnelError(msg)
		return
	}

	statusOut, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		setTunnelError("failed to get Tailscale status: " + err.Error())
		return
	}

	var tsStatus struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(statusOut, &tsStatus); err != nil {
		setTunnelError("failed to parse Tailscale status: " + err.Error())
		return
	}

	hostname := strings.TrimSuffix(tsStatus.Self.DNSName, ".")
	if hostname == "" {
		setTunnelError("could not determine Tailscale hostname — is Tailscale running?")
		return
	}

	tunnelMu.Lock()
	tunnelURL = fmt.Sprintf("https://%s", hostname)
	tunnelSt = "running"
	tunnelMu.Unlock()
	log.Printf("[tunnel] Tailscale funnel URL: %s", tunnelURL)
}

func setTunnelError(msg string) {
	tunnelMu.Lock()
	tunnelSt = "error"
	tunnelErr = msg
	tunnelMu.Unlock()
	log.Printf("[tunnel] error: %s", msg)
}
