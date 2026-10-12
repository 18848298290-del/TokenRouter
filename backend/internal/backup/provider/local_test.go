package provider

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLocalRoots routes archive operations through internal and external directory
// symlinks in isolated roots. External access must fail and retain the outside
// fixture, while in-root aliases must support upload, download, and deletion.
func TestLocalRoots(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	s := NewLocalBackupStore(root)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "fixture"), []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "outside")))
	_, err := s.Download(context.Background(), "outside/fixture")
	require.Error(t, err)
	_, err = s.Upload(context.Background(), "outside/new", bytes.NewBufferString("escaped"), "")
	require.Error(t, err)
	require.Error(t, s.Delete(context.Background(), "outside/fixture"))
	data, err := os.ReadFile(filepath.Join(outside, "fixture"))
	require.NoError(t, err)
	require.Equal(t, "outside", string(data))
	require.NoError(t, os.Mkdir(filepath.Join(root, "inside"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(root, "inside"), filepath.Join(root, "alias")))
	_, err = s.Upload(context.Background(), "alias/new", bytes.NewBufferString("safe"), "")
	require.NoError(t, err)
	body, err := s.Download(context.Background(), "alias/new")
	require.NoError(t, err)
	data, err = io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "safe", string(data))
	require.NoError(t, s.Delete(context.Background(), "alias/new"))
}

// TestPathReplacement replaces a validated target's parent with an outside symlink
// after obtaining its root handle. Root-based creation must reject that changed
// path, and the outside directory must remain without the requested file.
func TestPathReplacement(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	s := NewLocalBackupStore(base)
	require.NoError(t, os.Mkdir(filepath.Join(base, "dir"), 0o700))
	root, relative, err := s.rooted("dir/new")
	require.NoError(t, err)
	defer func() { _ = root.Close() }()
	// Change the directory after validation but keep using the original root handle,
	// exercising the gap between metadata checks and the filesystem operation itself.
	require.NoError(t, os.Rename(filepath.Join(base, "dir"), filepath.Join(base, "original")))
	require.NoError(t, os.Symlink(outside, filepath.Join(base, "dir")))
	file, err := root.Create(relative)
	if file != nil {
		// Close any unexpectedly returned handle before checking the rejection assertion,
		// so a regression that creates a file still leaves the test's resource lifetime bounded.
		_ = file.Close()
	}
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(outside, "new"))
	require.True(t, os.IsNotExist(err))
}

// TestDeleteInnerLinkPreservesTarget deletes an accepted final symlink to an
// in-root archive file. The named link must disappear while the target retains
// its original contents, protecting entry-based deletion after confinement checks.
func TestDeleteInnerLinkPreservesTarget(t *testing.T) {
	base := t.TempDir()
	s := NewLocalBackupStore(base)
	require.NoError(t, os.WriteFile(filepath.Join(base, "target"), []byte("kept"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(base, "target"), filepath.Join(base, "link")))
	require.NoError(t, s.Delete(context.Background(), "link"))
	data, err := os.ReadFile(filepath.Join(base, "target"))
	require.NoError(t, err)
	require.Equal(t, "kept", string(data))
	_, err = os.Lstat(filepath.Join(base, "link"))
	require.True(t, os.IsNotExist(err))
}

// TestLocalMissingReadDoesNotCreateRoot points the store at an absent root and
// requests a missing archive. Download must fail and deletion must succeed while
// both operations leave the configured root directory uncreated.
func TestLocalMissingReadDoesNotCreateRoot(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")
	s := NewLocalBackupStore(base)
	_, err := s.Download(context.Background(), "absent")
	require.Error(t, err)
	require.NoError(t, s.Delete(context.Background(), "absent"))
	_, err = os.Stat(base)
	require.True(t, os.IsNotExist(err))
}
