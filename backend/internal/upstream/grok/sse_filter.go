package grok

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	wireopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Bound candidate ping buffering so an unterminated frame cannot grow indefinitely.
// Frames exceeding either limit pass through unchanged.
const (
	ResponsesPingFrameMaxLines = 16
	ResponsesPingFrameMaxBytes = 16 * 1024
)

// Strict Responses clients can reject unknown event: ping frames injected by
// upstream billing or keepalive gateways. An SSE comment keeps the connection
// active without introducing an application event.
var responsesPingComment = []byte(": ping\n\n")

type ResponsesBillingPingFilterBody struct {
	*io.PipeReader
	source    io.Closer
	closeOnce sync.Once
	closeErr  error
}

func (b *ResponsesBillingPingFilterBody) Close() error {
	readerErr := b.PipeReader.Close()
	sourceErr := b.closeSource()
	if readerErr != nil {
		return readerErr
	}
	return sourceErr
}

func (b *ResponsesBillingPingFilterBody) closeSource() error {
	b.closeOnce.Do(func() { b.closeErr = b.source.Close() })
	return b.closeErr
}

func NewGrokResponsesBillingPingFilterBody(source io.ReadCloser, maxLineSize int) io.ReadCloser {
	reader, writer := io.Pipe()
	body := &ResponsesBillingPingFilterBody{PipeReader: reader, source: source}
	go FilterGrokResponsesBillingPings(source, writer, body.closeSource, maxLineSize)
	return body
}

func FilterGrokResponsesBillingPings(
	source io.Reader,
	destination *io.PipeWriter,
	closeSource func() error,
	maxLineSize int,
) {
	defer func() { _ = closeSource() }()
	if maxLineSize <= 0 {
		maxLineSize = upstream.DefaultSSELineLimit
	}

	scanner := bufio.NewScanner(source)
	scanBuf := httpclient.GetSSEScannerBuf64K()
	defer httpclient.PutSSEScannerBuf64K(scanBuf)
	initialBufferSize := min(maxLineSize, len(scanBuf))
	scanner.Buffer(scanBuf[:0:initialBufferSize], maxLineSize)
	scanner.Split(ScanSSELinesPreservingEndings)

	// Buffer candidates beginning with event: ping; pass other frames through line by line.
	pingFrame := make([][]byte, 0, 3)
	pingFrameBytes := 0
	inPassthroughFrame := false

	replayPingFrame := func() error {
		for _, line := range pingFrame {
			if _, err := destination.Write(line); err != nil {
				return err
			}
		}
		pingFrame = pingFrame[:0]
		pingFrameBytes = 0
		return nil
	}
	// Replace recognized pings with comments and replay other candidates byte for byte.
	// blankLine is nil when the stream ends within a frame.
	endPingFrame := func(blankLine []byte) error {
		if IsGrokResponsesPingEventFrame(pingFrame) {
			pingFrame = pingFrame[:0]
			pingFrameBytes = 0
			_, err := destination.Write(responsesPingComment)
			return err
		}
		if err := replayPingFrame(); err != nil {
			return err
		}
		if blankLine == nil {
			return nil
		}
		_, err := destination.Write(blankLine)
		return err
	}
	abort := func(err error) { _ = destination.CloseWithError(err) }

	for scanner.Scan() {
		line := scanner.Bytes()
		isBlank := len(TrimSSELineEnding(line)) == 0

		if inPassthroughFrame {
			if _, err := destination.Write(line); err != nil {
				abort(err)
				return
			}
			if isBlank {
				inPassthroughFrame = false
			}
			continue
		}

		if len(pingFrame) > 0 {
			if isBlank {
				if err := endPingFrame(line); err != nil {
					abort(err)
					return
				}
				continue
			}
			if CanExtendGrokResponsesPingFrame(line) &&
				len(pingFrame) < ResponsesPingFrameMaxLines &&
				pingFrameBytes+len(line) <= ResponsesPingFrameMaxBytes {
				pingFrame = append(pingFrame, append([]byte(nil), line...))
				pingFrameBytes += len(line)
				continue
			}
			// Unexpected fields or buffer limits disqualify the frame. Replay buffered bytes
			// and pass the remainder of this frame through unchanged.
			if err := replayPingFrame(); err != nil {
				abort(err)
				return
			}
			if _, err := destination.Write(line); err != nil {
				abort(err)
				return
			}
			inPassthroughFrame = true
			continue
		}

		// Only event: ping at the start of a frame begins a candidate.
		if !isBlank {
			if value, ok := wireopenai.ExtractSSEEventLine(string(TrimSSELineEnding(line))); ok && value == "ping" {
				pingFrame = append(pingFrame, append([]byte(nil), line...))
				pingFrameBytes = len(line)
				continue
			}
		}
		if _, err := destination.Write(line); err != nil {
			abort(err)
			return
		}
		inPassthroughFrame = !isBlank
	}
	if len(pingFrame) > 0 {
		if err := endPingFrame(nil); err != nil {
			abort(err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		abort(fmt.Errorf("filter Grok Responses billing ping: %w", err))
		return
	}
	_ = destination.Close()
}

func ScanSSELinesPreservingEndings(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for index, value := range data {
		switch value {
		case '\n':
			return index + 1, data[:index+1], nil
		case '\r':
			if index+1 == len(data) && !atEOF {
				return 0, nil, nil
			}
			if index+1 < len(data) && data[index+1] == '\n' {
				return index + 2, data[:index+2], nil
			}
			return index + 1, data[:index+1], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func TrimSSELineEnding(line []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
}

// CanExtendGrokResponsesPingFrame accepts data lines and comments within a candidate.
// Additional event, id, or retry fields disqualify the frame.
func CanExtendGrokResponsesPingFrame(rawLine []byte) bool {
	line := TrimSSELineEnding(rawLine)
	if len(line) > 0 && line[0] == ':' {
		return true
	}
	_, ok := wireopenai.ExtractSSEDataLine(string(line))
	return ok
}

// IsGrokResponsesPingEventFrame checks a candidate beginning with event: ping.
// A different data.type preserves the frame; absent, malformed, or ping data
// is treated as keepalive content for conversion to an SSE comment.
func IsGrokResponsesPingEventFrame(rawLines [][]byte) bool {
	dataParts := make([]string, 0, 1)
	for _, rawLine := range rawLines[1:] {
		if value, ok := wireopenai.ExtractSSEDataLine(string(TrimSSELineEnding(rawLine))); ok {
			dataParts = append(dataParts, value)
		}
	}
	if len(dataParts) == 0 {
		return true
	}
	var payload struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal([]byte(strings.Join(dataParts, "\n")), &payload); err != nil || payload.Type == nil {
		return true
	}
	return *payload.Type == "ping"
}
