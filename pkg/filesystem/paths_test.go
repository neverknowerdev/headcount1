package filesystem

import (
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	p := NewPaths("/base")

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"SettingsFile", p.SettingsFile(), "/base/settings.yaml"},
		{"DBDir", p.DBDir(), "/base/db"},
		{"DBFile", p.DBFile("headcount1.db"), "/base/db/headcount1.db"},
		{"SSHDir", p.SSHDir(), "/base/ssh"},
		{"SSHKeyFile", p.SSHKeyFile(), "/base/ssh/id_rsa"},
		{"CredentialsDir", p.CredentialsDir(), "/base/credentials"},
		{"UploadsDir", p.UploadsDir(), "/base/uploads"},
		{"TaskUploadsDir", p.TaskUploadsDir(7), "/base/uploads/7"},
		{"BackupsDir", p.BackupsDir(), "/base/backups"},
		{"ArchiveDir", p.ArchiveDir(), "/base/archive"},
		{"VenvDir", p.VenvDir(), "/base/venv"},
		{"RepoDir", p.RepoDir("acme", "proj"), "/base/repos/acme/proj"},
		{"WorktreeDir", p.WorktreeDir("acme", 42), "/base/workspace/acme/task-42"},
		{"TaskArtifactsDir", p.TaskArtifactsDir("acme", 42), "/base/artifacts/acme/42"},
		{"RunLogsDir", p.RunLogsDir("acme", 42, 5), "/base/logs/acme/42/run-5"},
		{"SkillDir", p.SkillDir("acme", "review"), "/base/skills/acme/review"},
	}

	for _, c := range cases {
		if c.got != filepath.FromSlash(c.want) {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	dirs := p.CompanyDirs("acme")
	want := []string{"/base/repos/acme", "/base/workspace/acme", "/base/artifacts/acme", "/base/logs/acme", "/base/skills/acme"}
	if len(dirs) != len(want) {
		t.Fatalf("CompanyDirs len = %d, want %d", len(dirs), len(want))
	}
	for i := range dirs {
		if dirs[i] != filepath.FromSlash(want[i]) {
			t.Errorf("CompanyDirs[%d] = %q, want %q", i, dirs[i], want[i])
		}
	}
}
