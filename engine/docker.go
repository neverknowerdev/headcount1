package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"agent-orchestrator/db"
)

type DockerSandbox struct {
	ImageName     string
	ContainerName string
	Port          int
}

func (d *DockerSandbox) BuildImage(ctx context.Context, dockerfilePath string) error {
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", d.ImageName, "-f", dockerfilePath, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build failed: %v\nOutput: %s", err, string(out))
	}
	return nil
}

func (d *DockerSandbox) Run(ctx context.Context, workspacePath string) error {
	// First, try to remove if exists
	exec.CommandContext(ctx, "docker", "rm", "-f", d.ContainerName).Run()

	// Mount opencode config directory so the container has provider configuration
	opencodeConfigDir := db.OpencodeConfigDir()
	paperclipHome := db.PaperclipHome()

	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", d.ContainerName,
		"-v", fmt.Sprintf("%s:/workspace", workspacePath),
		"-v", fmt.Sprintf("%s:/root/.config/opencode", opencodeConfigDir),
		"-v", fmt.Sprintf("%s:/root/.paperclip2", paperclipHome),
		"-w", "/workspace",
		"-p", fmt.Sprintf("%d:36000", d.Port),
		d.ImageName,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker run failed: %v\nOutput: %s", err, string(out))
	}

	// Wait for server to come up with 30 second timeout
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", d.Port)
	pingClient := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		if resp, err := pingClient.Get(baseURL + "/ping"); err == nil {
			resp.Body.Close()
			// Verify session endpoint is also ready
			testClient := &http.Client{Timeout: 5 * time.Second}
			testResp, err := testClient.Post(baseURL+"/session", "application/json", bytes.NewBufferString(`{"title":"healthcheck"}`))
			if err == nil {
				testResp.Body.Close()
				if testResp.StatusCode == 200 {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("docker sandbox server did not become ready within 30 seconds at %s", baseURL)
}

func (d *DockerSandbox) Stop() {
	exec.Command("docker", "rm", "-f", d.ContainerName).Run()
}
