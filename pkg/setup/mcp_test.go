package setup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMCPDependencyManifestSelectsReleaseAssets(t *testing.T) {
	manifest, err := loadMCPDependencyManifest()
	if err != nil {
		t.Fatal(err)
	}
	dependency, ok := manifest.Commands["github-mcp-server"]
	if !ok {
		t.Fatal("github-mcp-server dependency missing from manifest")
	}
	tests := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "github-mcp-server_Linux_x86_64.tar.gz"},
		{"darwin", "arm64", "github-mcp-server_Darwin_arm64.tar.gz"},
		{"windows", "386", "github-mcp-server_Windows_i386.zip"},
	}
	for _, test := range tests {
		asset, err := dependency.releaseAsset(test.goos, test.goarch)
		if err != nil || asset.Name != test.want {
			t.Errorf("releaseAsset(%q, %q) = %q, %v; want %q", test.goos, test.goarch, asset.Name, err, test.want)
		}
	}
	if _, err := dependency.releaseAsset("plan9", "amd64"); err == nil {
		t.Fatal("unsupported platform should fail")
	}
}

func TestExtractMCPArchiveTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "server.tar.gz")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	payload := []byte("github mcp")
	if err := tw.WriteHeader(&tar.Header{Name: "release/github-mcp-server", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	archive.Close()

	destination := filepath.Join(dir, "bin", "github-mcp-server")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractMCPArchiveTarGz(archivePath, destination, "github-mcp-server"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(destination); string(got) != string(payload) {
		t.Fatalf("extracted payload = %q", got)
	}
}

func TestExtractMCPArchiveZip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "server.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(archive)
	entry, err := zw.Create("release/github-mcp-server.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("github mcp")); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	archive.Close()

	destination := filepath.Join(dir, "bin", "github-mcp-server.exe")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractMCPArchiveZip(archivePath, destination, "github-mcp-server.exe"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(destination); string(got) != "github mcp" {
		t.Fatalf("extracted payload = %q", got)
	}
}

func TestInstallMCPCommandDependencyDownloadsIntoApplicationBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar fixture exercises Unix release installation")
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	payload := []byte("#!/bin/sh\nexit 0\n")
	if err := tw.WriteHeader(&tar.Header{Name: "github-mcp-server", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(archive.Bytes())
	}))
	defer server.Close()
	t.Setenv("E2E_HEADCOUNT1_HOME", t.TempDir())
	t.Setenv("PATH", "")
	dependency := MCPCommandDependency{
		Installer:      "release-archive",
		Executable:     "github-mcp-server",
		ReleaseBaseURL: server.URL,
		Version:        "test",
		Assets: []MCPReleaseAsset{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Name: "server.tar.gz", Archive: "tar.gz", Binary: "github-mcp-server",
		}},
	}
	if err := installMCPCommandDependency(t.Context(), dependency, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dbHomeForTest(t), ".headcount1", "bin", "github-mcp-server")
	if got, err := os.ReadFile(installed); err != nil || string(got) != string(payload) {
		t.Fatalf("installed executable = %q, %v", got, err)
	}
}

func dbHomeForTest(t *testing.T) string {
	t.Helper()
	return os.Getenv("E2E_HEADCOUNT1_HOME")
}
