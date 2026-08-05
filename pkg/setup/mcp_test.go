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

func TestGitHubMCPAsset(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "github-mcp-server_Linux_x86_64.tar.gz"},
		{"darwin", "arm64", "github-mcp-server_Darwin_arm64.tar.gz"},
		{"windows", "386", "github-mcp-server_Windows_i386.zip"},
	}
	for _, test := range tests {
		got, err := githubMCPAsset(test.goos, test.goarch)
		if err != nil || got != test.want {
			t.Errorf("githubMCPAsset(%q, %q) = %q, %v; want %q", test.goos, test.goarch, got, err, test.want)
		}
	}
	if _, err := githubMCPAsset("plan9", "amd64"); err == nil {
		t.Fatal("unsupported platform should fail")
	}
}

func TestExtractGitHubMCPTarGz(t *testing.T) {
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
	if err := extractGitHubMCPTarGz(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(destination); string(got) != string(payload) {
		t.Fatalf("extracted payload = %q", got)
	}
}

func TestExtractGitHubMCPZip(t *testing.T) {
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
	if err := extractGitHubMCPZip(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(destination); string(got) != "github mcp" {
		t.Fatalf("extracted payload = %q", got)
	}
}

func TestEnsureGitHubMCPServerDownloadsIntoApplicationBin(t *testing.T) {
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
	originalBaseURL := githubMCPReleaseBaseURL
	githubMCPReleaseBaseURL = server.URL
	defer func() { githubMCPReleaseBaseURL = originalBaseURL }()

	t.Setenv("E2E_HEADCOUNT1_HOME", t.TempDir())
	t.Setenv("PATH", "")
	if err := ensureGitHubMCPServer(t.Context()); err != nil {
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
