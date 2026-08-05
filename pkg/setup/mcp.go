package setup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"agent-orchestrator/db"
)

const githubMCPVersion = "v1.8.0"

var githubMCPReleaseBaseURL = "https://github.com/github/github-mcp-server/releases/download"

// InstallMCPDependencies installs dependencies declared by one MCP server.
// Keeping this server-scoped lets callers persist and display failures on the
// integration that owns them instead of failing the platform-wide setup.
func InstallMCPDependencies(ctx context.Context, server db.MCPServer) error {
	if err := InstallNpmDeps(ctx, server.Deps); err != nil {
		return err
	}
	if server.Name == "github" && server.Command == "github-mcp-server" {
		return ensureGitHubMCPServer(ctx)
	}
	return nil
}

func mcpBinDir() (string, error) {
	dir := filepath.Join(db.Headcount1Home(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == dir {
			return dir, nil
		}
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return "", err
	}
	return dir, nil
}

func ensureGitHubMCPServer(ctx context.Context) error {
	binDir, err := mcpBinDir()
	if err != nil {
		return fmt.Errorf("prepare MCP binary directory: %w", err)
	}
	if _, err := exec.LookPath("github-mcp-server"); err == nil {
		return nil
	}

	asset, err := githubMCPAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubMCPReleaseBaseURL+"/"+githubMCPVersion+"/"+asset, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: GitHub returned %s", asset, response.Status)
	}

	tmp, err := os.CreateTemp("", "headcount1-github-mcp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, io.LimitReader(response.Body, 200<<20)); err != nil {
		tmp.Close()
		return fmt.Errorf("save %s: %w", asset, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	destination := filepath.Join(binDir, "github-mcp-server")
	if runtime.GOOS == "windows" {
		destination += ".exe"
	}
	if strings.HasSuffix(asset, ".zip") {
		err = extractGitHubMCPZip(tmpPath, destination)
	} else {
		err = extractGitHubMCPTarGz(tmpPath, destination)
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", asset, err)
	}
	if _, err := exec.LookPath("github-mcp-server"); err != nil {
		return fmt.Errorf("installed executable is not available on PATH: %w", err)
	}
	return nil
}

func githubMCPAsset(goos, goarch string) (string, error) {
	osName := map[string]string{"darwin": "Darwin", "linux": "Linux", "windows": "Windows"}[goos]
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64", "386": "i386"}[goarch]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("GitHub MCP Server does not publish a binary for %s/%s", goos, goarch)
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("github-mcp-server_%s_%s%s", osName, arch, ext), nil
}

func extractGitHubMCPTarGz(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return fmt.Errorf("github-mcp-server executable not found in archive")
		}
		if err != nil {
			return err
		}
		if filepath.Base(header.Name) == "github-mcp-server" && header.Typeflag == tar.TypeReg {
			return writeExecutable(destination, reader)
		}
	}
}

func extractGitHubMCPZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if filepath.Base(file.Name) != "github-mcp-server.exe" {
			continue
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		err = writeExecutable(destination, source)
		source.Close()
		return err
	}
	return fmt.Errorf("github-mcp-server.exe not found in archive")
}

func writeExecutable(destination string, source io.Reader) error {
	tmp := destination + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, destination); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
