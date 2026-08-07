package setup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
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

//go:embed mcp_dependencies.json
var embeddedMCPDependencies []byte

// MCPDependencyManifest is the declarative source of non-NPM dependencies
// required by MCP commands. Each command can opt into a generic installer
// without coupling setup code to a particular integration.
type MCPDependencyManifest struct {
	Commands map[string]MCPCommandDependency `json:"commands"`
}

type MCPCommandDependency struct {
	Installer      string            `json:"installer"`
	Executable     string            `json:"executable"`
	ReleaseBaseURL string            `json:"release_base_url"`
	Version        string            `json:"version"`
	Assets         []MCPReleaseAsset `json:"assets"`
}

type MCPReleaseAsset struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Name    string `json:"name"`
	Archive string `json:"archive"`
	Binary  string `json:"binary"`
}

func loadMCPDependencyManifest() (MCPDependencyManifest, error) {
	var manifest MCPDependencyManifest
	if err := json.Unmarshal(embeddedMCPDependencies, &manifest); err != nil {
		return MCPDependencyManifest{}, fmt.Errorf("parse MCP dependency manifest: %w", err)
	}
	if manifest.Commands == nil {
		return MCPDependencyManifest{}, fmt.Errorf("MCP dependency manifest has no commands")
	}
	return manifest, nil
}

// InstallMCPDependencies installs dependencies declared by one MCP server.
// NPM dependencies remain data on MCPServer; binary dependencies are selected
// by command from mcp_dependencies.json. Adding a new managed binary requires
// only a manifest entry, not an integration-specific Go branch.
func InstallMCPDependencies(ctx context.Context, server db.MCPServer) error {
	if err := InstallNpmDeps(ctx, server.Deps); err != nil {
		return err
	}
	manifest, err := loadMCPDependencyManifest()
	if err != nil {
		return err
	}
	dependency, managed := manifest.Commands[server.Command]
	if !managed {
		return nil
	}
	return installMCPCommandDependency(ctx, dependency, runtime.GOOS, runtime.GOARCH)
}

func installMCPCommandDependency(ctx context.Context, dependency MCPCommandDependency, goos, goarch string) error {
	if dependency.Installer != "release-archive" {
		return fmt.Errorf("unsupported MCP dependency installer %q", dependency.Installer)
	}
	if dependency.Executable == "" || dependency.ReleaseBaseURL == "" || dependency.Version == "" {
		return fmt.Errorf("release-archive MCP dependency is missing executable, release_base_url, or version")
	}
	binDir, err := mcpBinDir()
	if err != nil {
		return fmt.Errorf("prepare MCP binary directory: %w", err)
	}
	if _, err := exec.LookPath(dependency.Executable); err == nil {
		return nil
	}

	asset, err := dependency.releaseAsset(goos, goarch)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(dependency.ReleaseBaseURL, "/")+"/"+dependency.Version+"/"+asset.Name, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: release server returned %s", asset.Name, response.Status)
	}

	tmp, err := os.CreateTemp("", "headcount1-mcp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, io.LimitReader(response.Body, 200<<20)); err != nil {
		tmp.Close()
		return fmt.Errorf("save %s: %w", asset.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	destination := filepath.Join(binDir, asset.Binary)
	switch asset.Archive {
	case "tar.gz":
		err = extractMCPArchiveTarGz(tmpPath, destination, asset.Binary)
	case "zip":
		err = extractMCPArchiveZip(tmpPath, destination, asset.Binary)
	default:
		err = fmt.Errorf("unsupported archive format %q", asset.Archive)
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", asset.Name, err)
	}
	if _, err := exec.LookPath(dependency.Executable); err != nil {
		return fmt.Errorf("installed executable is not available on PATH: %w", err)
	}
	return nil
}

func (d MCPCommandDependency) releaseAsset(goos, goarch string) (MCPReleaseAsset, error) {
	for _, asset := range d.Assets {
		if asset.OS == goos && asset.Arch == goarch {
			if asset.Name == "" || asset.Archive == "" || asset.Binary == "" {
				return MCPReleaseAsset{}, fmt.Errorf("MCP dependency asset for %s/%s is incomplete", goos, goarch)
			}
			return asset, nil
		}
	}
	return MCPReleaseAsset{}, fmt.Errorf("MCP dependency does not publish a binary for %s/%s", goos, goarch)
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

func extractMCPArchiveTarGz(archivePath, destination, binaryName string) error {
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
			return fmt.Errorf("%s executable not found in archive", binaryName)
		}
		if err != nil {
			return err
		}
		if filepath.Base(header.Name) == binaryName && header.Typeflag == tar.TypeReg {
			return writeExecutable(destination, reader)
		}
	}
}

func extractMCPArchiveZip(archivePath, destination, binaryName string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName {
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
	return fmt.Errorf("%s executable not found in archive", binaryName)
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
