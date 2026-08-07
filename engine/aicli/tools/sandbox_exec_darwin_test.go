//go:build darwin

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeatbeltProfileContents(t *testing.T) {
	workspace := t.TempDir()
	profile, err := seatbeltProfile(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(version 1)",
		"(allow default)",
		"(deny file-write*)",
		fmt.Sprintf("(subpath \"%s\")", workspace),
		`(literal "/dev/null")`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %q:\n%s", want, profile)
		}
	}
}

func TestEscapeSeatbeltString(t *testing.T) {
	got, err := escapeSeatbeltString(`/tmp/we"ird\dir`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `/tmp/we\"ird\\dir`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := escapeSeatbeltString("/tmp/bad\npath"); err == nil {
		t.Error("expected error for newline in path")
	}
}

func TestSeatbeltBlocksWriteOutsideWorkspace(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	workspace := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	outside, err := os.MkdirTemp(home, "sandbox-escape-test-*")
	if err != nil {
		t.Skipf("cannot create test dir in home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })

	args, err := json.Marshal(map[string]string{
		"command": fmt.Sprintf(`D=%s; echo pwned > "$D/pwned.txt" && echo WROTE || echo BLOCKED`, outside),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := NewExecCommand(workspace).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("expected write to be blocked, got output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Errorf("file was created outside the workspace (stat err: %v)", err)
	}

	args, err = json.Marshal(map[string]string{"command": `echo hello > file.txt && cat file.txt`})
	if err != nil {
		t.Fatal(err)
	}
	out, err = NewExecCommand(workspace).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected in-workspace write to succeed, got output: %q", out)
	}
}
