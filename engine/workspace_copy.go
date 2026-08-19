package engine

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyWorkspace makes a durable snapshot of a session workspace. It is used
// before a fork starts, so the fork can continue from the exact filesystem
// state at the selected conversation boundary without invoking any tools again.
func copyWorkspace(source, destination string) error {
	if source == "" || destination == "" {
		return fmt.Errorf("workspace source and destination are required")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve source workspace: %w", err)
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve destination workspace: %w", err)
	}
	if source == destination {
		return fmt.Errorf("workspace source and destination must differ")
	}
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("stat source workspace: %w", err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create destination workspace: %w", err)
	}

	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read symlink %s: %w", path, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported workspace entry %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := copyWorkspaceFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		return nil
	})
}

func copyWorkspaceFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open workspace file %s: %w", source, err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return fmt.Errorf("create workspace file %s: %w", destination, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy workspace file %s: %w", source, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close workspace file %s: %w", destination, err)
	}
	return nil
}
