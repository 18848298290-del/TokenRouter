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

// failedRestoreReader simulates corruption at the archive tail.
type failedRestoreReader struct{}

// TestRestoreActivityPrefixChecksWholeInput checks that the cleanup prefix and archive share one psql transaction.
func TestRestoreActivityPrefixChecksWholeInput(t *testing.T) {
	dir := t.TempDir()
	inputPath, argsPath := filepath.Join(dir, "input.sql"), filepath.Join(dir, "args")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TR_TEST_RESTORE_INPUT", inputPath)
	t.Setenv("TR_TEST_RESTORE_ARGS", argsPath)
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

func (failedRestoreReader) Read([]byte) (int, error) {
	return 0, errors.New("archive read failed")
}
