package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSpoolNextBackupPart_ReassemblesExactBytes splits a stream across five-byte
// spools and rejoins them. Each digest and size must describe its actual file,
// and the complete sequence must reproduce the source bytes exactly.
func TestSpoolNextBackupPart_ReassemblesExactBytes(t *testing.T) {
	reader := bufio.NewReader(bytes.NewReader([]byte("0123456789abcdefg")))
	var got bytes.Buffer
	for index := 1; ; index++ {
		part, hasPart, hasMore, err := spoolNextBackupPart(context.Background(), reader, index, 5)
		require.NoError(t, err)
		require.True(t, hasPart)
		data, err := os.ReadFile(part.Path)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(data)), part.SHA256)
		require.Equal(t, int64(len(data)), part.SizeBytes)
		// Remove each consumed spool before advancing so this scenario also observes
		// the caller's temporary-file ownership between successive bounded archive segment creations.
		require.NoError(t, cleanupBackupFiles(part.Path))
		_, err = got.Write(data)
		require.NoError(t, err)
		if !hasMore {
			break
		}
	}
	require.Equal(t, []byte("0123456789abcdefg"), got.Bytes())
}

// TestSpoolNextBackupPart_RejectsInvalidInput supplies nonpositive segment size
// and index values. Both calls must return errors before valid segment metadata
// can be produced for object-store upload planning.
func TestSpoolNextBackupPart_RejectsInvalidInput(t *testing.T) {
	reader := bufio.NewReader(bytes.NewReader([]byte("data")))
	_, _, _, err := spoolNextBackupPart(context.Background(), reader, 1, 0)
	require.Error(t, err)
	_, _, _, err = spoolNextBackupPart(context.Background(), reader, 0, 5)
	require.Error(t, err)
}

// TestSpoolNextBackupPart_EmptyInputCreatesNoFile directs an empty source into an
// isolated temporary directory. Spooling must return no part or continuation and
// leave that directory empty after disposing of its unused candidate file.
func TestSpoolNextBackupPart_EmptyInputCreatesNoFile(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	part, hasPart, hasMore, err := spoolNextBackupPart(
		context.Background(),
		bufio.NewReader(bytes.NewReader(nil)),
		1,
		5,
	)
	require.NoError(t, err)
	require.False(t, hasPart)
	require.False(t, hasMore)
	require.Empty(t, part.Path)
	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestSpoolNextBackupPart_CancelledContextRemovesTemporaryFile cancels before
// copying a nonempty source into its isolated temporary directory. The call must
// return context cancellation and remove its candidate spool from that directory.
func TestSpoolNextBackupPart_CancelledContextRemovesTemporaryFile(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := spoolNextBackupPart(
		ctx,
		bufio.NewReader(bytes.NewReader([]byte("backup"))),
		1,
		5,
	)
	require.ErrorIs(t, err, context.Canceled)
	entries, readErr := os.ReadDir(tempDir)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}
