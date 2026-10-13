package openai

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAIFirstOutputEventQueueSizeBackpressuresGuardedStreams compares
// guarded and ordinary queue selection. It asserts capacities of one and
// sixteen, protecting the configuration used to bound producer buffering before
// first output.
func TestOpenAIFirstOutputEventQueueSizeBackpressuresGuardedStreams(t *testing.T) {
	require.Equal(t, 1, OpenAIFirstOutputEventQueueSize(true))
	require.Equal(t, 16, OpenAIFirstOutputEventQueueSize(false))
}

// TestOpenAIFirstOutputDynamicScannerLimitsOnlyWhileGuardIsActive supplies an
// undelimited token at the guarded limit. The active splitter must reject it,
// while deactivating the flag permits the same bytes to await a delimiter.
func TestOpenAIFirstOutputDynamicScannerLimitsOnlyWhileGuardIsActive(t *testing.T) {
	var guardActive atomic.Bool
	guardActive.Store(true)
	split := OpenAIFirstOutputDynamicScanLines(&guardActive)
	guardLimit := OpenAIFirstOutputStageMaxBytes + OpenAIFirstOutputScannerFramingAllowance
	undelimited := bytes.Repeat([]byte("x"), guardLimit)

	_, _, err := split(undelimited, false)
	require.ErrorIs(t, err, ErrOpenAIFirstOutputScannerLimit)

	guardActive.Store(false)
	advance, token, err := split(undelimited, false)
	require.NoError(t, err)
	require.Zero(t, advance)
	require.Nil(t, token)
}

// TestOpenAIFirstOutputStageOverflowIsAtomicAndCleanupRemovesSpool stages a
// large payload, then attempts an over-budget append. It checks unchanged
// accepted size, platform storage choice, Unix unlink and permissions, and
// completed cleanup.
func TestOpenAIFirstOutputStageOverflowIsAtomicAndCleanupRemovesSpool(t *testing.T) {
	stage := NewOpenAIFirstOutputStage(70 * 1024)
	payload := bytes.Repeat([]byte("x"), 68*1024)
	n, err := stage.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	if runtime.GOOS == "windows" {
		require.Nil(t, stage.tempFile)
		require.Empty(t, stage.tempPath)
	} else {
		require.NotNil(t, stage.tempFile)
		require.NotEmpty(t, stage.tempPath)
		_, err = os.Stat(stage.tempPath)
		require.ErrorIs(t, err, os.ErrNotExist)
		stat, statErr := stage.tempFile.Stat()
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), stat.Mode().Perm())
	}

	n, err = stage.Write(bytes.Repeat([]byte("y"), 3*1024))
	require.Zero(t, n)
	require.ErrorIs(t, err, ErrOpenAIFirstOutputStageLimit)
	require.EqualValues(t, len(payload), stage.Buffered())
	path := stage.tempPath
	require.NoError(t, stage.Close())
	require.True(t, stage.closed)
	require.Nil(t, stage.tempFile)
	require.Empty(t, stage.tempPath)
	if path != "" {
		_, err = os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

// TestOpenAIFirstOutputStageCommitCopiesSpoolAndRemovesTemp stages a large
// payload and commits it to a downstream buffer. It protects complete byte
// delivery, platform spool behavior, reset buffering, and repeatable cleanup.
func TestOpenAIFirstOutputStageCommitCopiesSpoolAndRemovesTemp(t *testing.T) {
	stage := NewOpenAIFirstOutputStage(80 * 1024)
	payload := bytes.Repeat([]byte("z"), 68*1024)
	_, err := stage.Write(payload)
	require.NoError(t, err)
	path := stage.tempPath
	if runtime.GOOS == "windows" {
		require.Empty(t, path)
		require.Nil(t, stage.tempFile)
	} else {
		require.NotEmpty(t, path)
		require.NotNil(t, stage.tempFile)
		_, statErr := os.Stat(path)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	}

	var downstream bytes.Buffer
	require.NoError(t, stage.CommitTo(&downstream))
	require.Equal(t, payload, downstream.Bytes())
	require.Zero(t, stage.Buffered())
	if path != "" {
		_, err = os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	require.NoError(t, stage.Close())
}

// TestOpenAIFirstOutputStageUnlinkFailurePermanentlyFallsBackToMemoryAndRetriesCleanup
// injects two initial removal failures. It checks that the named file remains
// empty, later writes stay in memory, and Close removes the path while
// returning retained cleanup evidence.
func TestOpenAIFirstOutputStageUnlinkFailurePermanentlyFallsBackToMemoryAndRetriesCleanup(t *testing.T) {
	stage := NewDefaultOpenAIFirstOutputStage()
	stage.memoryOnly = false
	// Restore real removal before final cleanup so an assertion failure cannot
	// leave the intentionally retained empty temporary path behind after this fixture.
	t.Cleanup(func() {
		stage.removeFile = os.Remove
		_ = stage.Close()
	})
	createCalls := 0
	stage.createTemp = func() (*os.File, error) {
		createCalls++
		return os.CreateTemp("", "tokenrouter-openai-first-output-fallback-*")
	}
	removeCalls := 0
	// Fail initial unlink and its immediate cleanup retry to retain a named
	// empty file; the next removal succeeds when Close retries that same path.
	stage.removeFile = func(path string) error {
		removeCalls++
		if removeCalls <= 2 {
			return errors.New("forced remove failure")
		}
		return os.Remove(path)
	}

	payload := bytes.Repeat([]byte("m"), 68*1024)
	_, err := stage.Write(payload)
	require.NoError(t, err)
	require.True(t, stage.memoryOnly)
	require.Nil(t, stage.tempFile)
	require.NotEmpty(t, stage.tempPath)
	require.Equal(t, 1, createCalls)
	stat, statErr := os.Stat(stage.tempPath)
	require.NoError(t, statErr)
	require.Zero(t, stat.Size(), "failed-unlink fallback must never write plaintext to the named file")

	_, err = stage.WriteString("more")
	require.NoError(t, err)
	require.Equal(t, 1, createCalls, "memory-only fallback must not retry CreateTemp")
	path := stage.tempPath
	cleanupErr := stage.Close()
	require.ErrorContains(t, cleanupErr, "forced remove failure")
	require.Empty(t, stage.tempPath)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, stage.Close())
}
