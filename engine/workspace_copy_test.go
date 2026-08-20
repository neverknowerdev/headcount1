package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyWorkspaceCopiesNestedFilesModesAndSymlinks(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "fork")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "nested", "state.txt"), []byte("tool result"), 0o640))
	require.NoError(t, os.Symlink("nested/state.txt", filepath.Join(source, "state-link")))

	require.NoError(t, copyWorkspace(source, destination))
	content, err := os.ReadFile(filepath.Join(destination, "nested", "state.txt"))
	require.NoError(t, err)
	require.Equal(t, "tool result", string(content))
	info, err := os.Stat(filepath.Join(destination, "nested", "state.txt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	link, err := os.Readlink(filepath.Join(destination, "state-link"))
	require.NoError(t, err)
	require.Equal(t, "nested/state.txt", link)
}

func TestCopyWorkspaceRejectsMissingOrSameWorkspace(t *testing.T) {
	source := t.TempDir()
	require.Error(t, copyWorkspace(filepath.Join(source, "missing"), filepath.Join(source, "fork")))
	require.Error(t, copyWorkspace(source, source))
}
