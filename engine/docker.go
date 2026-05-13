package engine

import (
	"context"
	"fmt"
	"os/exec"
	"time"
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

	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", d.ContainerName,
		"-v", fmt.Sprintf("%s:/workspace", workspacePath),
		"-w", "/workspace",
		"-p", fmt.Sprintf("%d:36000", d.Port),
		d.ImageName,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker run failed: %v\nOutput: %s", err, string(out))
	}

	// Wait for server to come up
	time.Sleep(3 * time.Second)
	return nil
}

func (d *DockerSandbox) Stop() {
	exec.Command("docker", "rm", "-f", d.ContainerName).Run()
}
