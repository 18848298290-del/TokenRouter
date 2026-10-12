package upstream

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// TestGrokResponsesClientToolStreamBodyFlushesFrameBeforeEOF sends a complete
// frame through a source pipe that stays open. It checks that the restored
// reader receives the frame and event label before source EOF.
func TestGrokResponsesClientToolStreamBodyFlushesFrameBeforeEOF(t *testing.T) {
	sourceReader, sourceWriter := io.Pipe()
	body := NewResponsesClientToolStreamBody(sourceReader, bridge.ResponsesClientToolMapping{
		CustomTools: map[string]bool{"apply_patch": true},
	}, 500*1024*1024)
	// Cleanup closes both pipe directions even when the bounded wait fails,
	// releasing source and transformed reads that can still be pending in workers.
	defer func() { _ = body.Close() }()
	defer func() { _ = sourceWriter.Close() }()

	// readResult carries the first transformed frame or its asynchronous read
	// failure.
	type readResult struct {
		frame string // first complete transformed frame
		err   error  // asynchronous frame read failure
	}
	// One buffered result lets the reader exit if the timeout wins first.
	// Keeping the source writer open exercises frame flushing rather than EOF cleanup.
	read := make(chan readResult, 1)
	go func() {
		reader := bufio.NewReader(body)
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				read <- readResult{err: err}
				return
			}
			if _, err := frame.WriteString(line); err != nil {
				read <- readResult{err: err}
				return
			}
			if strings.TrimSpace(line) == "" {
				read <- readResult{frame: frame.String()}
				return
			}
		}
	}()

	firstFrame := "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"flush-before-eof"}}` + "\n\n"
	_, err := sourceWriter.Write([]byte(firstFrame))
	require.NoError(t, err)

	select {
	case result := <-read:
		require.NoError(t, result.err)
		require.Contains(t, result.frame, "flush-before-eof")
		require.Contains(t, result.frame, "event: response.created")
	// Bound the fixture wait at three seconds.
	case <-time.After(3 * time.Second):
		t.Fatal("first transformed SSE frame was not flushed while the upstream connection remained open")
	}
}
