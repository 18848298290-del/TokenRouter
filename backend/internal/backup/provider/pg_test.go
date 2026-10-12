//go:build !windows

package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// failedRestoreReader reports an injected input error after earlier archive bytes are consumed.
type failedRestoreReader struct{}

// TestRestoreActivityPrefixChecksWholeInput captures the restore command's flags
// and prefixed stdin using a shell psql shim. It checks transaction-option requests,
// empty-input rejection, and propagation of a tail read failure; the shim does
// not execute SQL or demonstrate a database rollback.
func TestRestoreActivityPrefixChecksWholeInput(t *testing.T) {
	dir := t.TempDir()
	inputPath, argsPath := filepath.Join(dir, "input.sql"), filepath.Join(dir, "args")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TR_TEST_RESTORE_INPUT", inputPath)
	t.Setenv("TR_TEST_RESTORE_ARGS", argsPath)
	// Substitute a client that records argv and stdin instead of connecting to a
	// database, keeping these assertions focused on process construction and archive input handling.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "psql"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TR_TEST_RESTORE_ARGS\"\ncat > \"$TR_TEST_RESTORE_INPUT\"\n"), 0o755))
	dumper := NewPgDumper(DatabaseOptions{DBName: "test", Port: 5432})
	const archive = "SELECT 'archive';\n"
	require.NoError(t, dumper.Restore(context.Background(), strings.NewReader(archive)))
	input, err := os.ReadFile(inputPath)
	require.NoError(t, err)
	require.Equal(t, "DROP TABLE IF EXISTS public.usage_user_activity;\n"+archive, string(input))
	args, err := os.ReadFile(argsPath)
	require.NoError(t, err)
	require.Contains(t, string(args), "--single-transaction")
	require.Contains(t, string(args), "--set=ON_ERROR_STOP=1")
	require.ErrorIs(t, dumper.Restore(context.Background(), strings.NewReader("")), io.EOF)
	err = dumper.Restore(context.Background(), io.MultiReader(strings.NewReader(archive), failedRestoreReader{}))
	require.ErrorContains(t, err, "archive read failed")
}

// Read returns the fixture's tail-input failure without consuming any bytes.
func (failedRestoreReader) Read([]byte) (int, error) {
	return 0, errors.New("archive read failed")
}
