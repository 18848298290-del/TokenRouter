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

// Grok billing gateways can insert event: ping frames into a Responses stream,
// and strict clients may interpret that event as an unsupported application
// message. A recognized ping becomes an SSE comment, which retains keepalive
// traffic while ordinary Responses events and their original line endings pass
// to the shared reader.
//
// Recognition needs the whole candidate frame because a later data.type can
// contradict the initial ping event. The filter copies candidate lines across
// scanner advances and caps extensions by both line count and bytes. Unknown
// fields or exceeded caps replay those copies and make the rest of that frame
// pass through. A pipe connects the filtering goroutine to its reader; closing
// the reader interrupts writes, and sync.Once gives worker completion and caller
// cleanup one shared source-close result.

// ResponsesPingFrameMaxLines and ResponsesPingFrameMaxBytes limit extensions of
// a buffered candidate; exceeding either cap replays it and passes the frame
// through.
const (
	ResponsesPingFrameMaxLines = 16
	ResponsesPingFrameMaxBytes = 16 * 1024
)

// responsesPingComment is the replacement SSE comment written for a recognized
// billing ping, keeping stream activity visible to downstream connection
// readers.
var responsesPingComment = []byte(": ping\n\n")

// ResponsesBillingPingFilterBody connects filtered stream reads to coordinated
// source cleanup.
type ResponsesBillingPingFilterBody struct {
	// PipeReader belongs to the returned body; consuming or closing it releases
	// filter writes that would otherwise wait for the caller's next read.
	*io.PipeReader // filtered stream reader
	// source, closeOnce and closeErr share cleanup between the filtering goroutine
	// and reader closure; closeOnce saves the first source.Close result in
	// closeErr.
	source    io.Closer // owned upstream closer
	closeOnce sync.Once // source cleanup coordination
	closeErr  error     // cached source-close error
}

// Close closes the pipe reader and then closes the source through shared
// cleanup. The concrete io.PipeReader close returns nil, so this path returns
// the cached source-close result. Reader closure releases any filter write
// blocked on consumption.
func (b *ResponsesBillingPingFilterBody) Close() error {
	readerErr := b.PipeReader.Close()
	sourceErr := b.closeSource()
	if readerErr != nil {
		return readerErr
	}
	return sourceErr
}

// closeSource closes the underlying source once and returns its cached error.
// The filter worker and the body's Close method share this operation, so either
// completion path can release the same source.
func (b *ResponsesBillingPingFilterBody) closeSource() error {
	b.closeOnce.Do(func() { b.closeErr = b.source.Close() })
	return b.closeErr
}

// NewGrokResponsesBillingPingFilterBody starts a filtering goroutine and returns
// its pipe reader. Callers consume or close the returned body to release pipe
// writes and source ownership. Nonpositive maxLineSize selects the standard SSE
// line limit.
func NewGrokResponsesBillingPingFilterBody(source io.ReadCloser, maxLineSize int) io.ReadCloser {
	reader, writer := io.Pipe()
	body := &ResponsesBillingPingFilterBody{PipeReader: reader, source: source}
	go FilterGrokResponsesBillingPings(source, writer, body.closeSource, maxLineSize)
	return body
}

// FilterGrokResponsesBillingPings writes a stream whose recognized billing pings
// become comments. It owns destination completion and invokes closeSource when
// processing ends; destination errors stop reading. Nonpositive maxLineSize uses
// DefaultSSELineLimit, and scanner storage is returned after scanning finishes.
//
// A frame-leading ping starts copied candidate lines. Data or comments can
// extend it within both caps; another field or excess buffering replays the
// candidate and passes the frame through. At a separator or EOF, a conflicting
// decoded type retains the original bytes. Scanner errors reach readers through
// CloseWithError after any pending candidate is resolved.
func FilterGrokResponsesBillingPings(
	source io.Reader,
	destination *io.PipeWriter,
	closeSource func() error,
	maxLineSize int,
) {
	// The worker releases source ownership on every exit, sharing cleanup with
	// caller Close so either path can end the read.
	defer func() { _ = closeSource() }()
	if maxLineSize <= 0 {
		maxLineSize = upstream.DefaultSSELineLimit
	}

	scanner := bufio.NewScanner(source)
	scanBuf := httpclient.GetSSEScannerBuf64K()
	// Keep scanner storage borrowed until filtering ends; retained ping lines use
	// their own copies and outlive individual scanner tokens.
	defer httpclient.PutSSEScannerBuf64K(scanBuf)
	initialBufferSize := min(maxLineSize, len(scanBuf))
	scanner.Buffer(scanBuf[:0:initialBufferSize], maxLineSize)
	scanner.Split(ScanSSELinesPreservingEndings)

	// Copy candidate lines before the scanner advances; its reusable buffer would
	// otherwise overwrite bytes needed when a later field disqualifies the ping.
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
	// Decide after the complete candidate is available because data.type can name a
	// different application event. A nil blankLine represents the EOF-only path.
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
			// An unexpected field or exceeded cap means conversion is unsafe for this
			// candidate. Replay saved bytes, then keep the rest of its frame intact.
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

		// A frame's first event identifies a possible billing ping; events encountered
		// after another field stay in passthrough to retain their application meaning.
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

// ScanSSELinesPreservingEndings splits LF, CRLF and lone-CR lines with
// terminators included. Tokens borrow scanner input storage, so callers copy
// bytes they retain across subsequent scans. At EOF the remaining unterminated
// bytes form a final token.
//
// A CR at the current buffer end waits for more input before choosing its
// terminator length. This keeps a CRLF split across reads together, allowing
// disqualified ping candidates and ordinary events to replay their original line
// endings.
func ScanSSELinesPreservingEndings(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for index, value := range data {
		switch value {
		case '\n':
			return index + 1, data[:index+1], nil
		case '\r':
			// Wait for the next read to reveal a following LF; treating this CR
			// immediately would split one CRLF terminator.
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

// TrimSSELineEnding removes one trailing LF and then one trailing CR from a
// line. Its result shares the original storage, and callers use it for frame
// interpretation while retaining the terminated bytes for forwarding.
func TrimSSELineEnding(line []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
}

// CanExtendGrokResponsesPingFrame admits comment and data lines inside an
// existing ping candidate. Callers identify the leading event separately;
// additional event, id, retry or unknown fields make the candidate return to
// byte-preserving forwarding.
func CanExtendGrokResponsesPingFrame(rawLine []byte) bool {
	line := TrimSSELineEnding(rawLine)
	if len(line) > 0 && line[0] == ':' {
		return true
	}
	_, ok := wireopenai.ExtractSSEDataLine(string(line))
	return ok
}

// IsGrokResponsesPingEventFrame checks data fields of a nonempty candidate
// already beginning with event: ping. Absent data, a JSON/type decoding error,
// or a missing/null type permits conversion. A successfully decoded string type
// other than ping retains the application frame.
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
		// Type distinguishes a decoded event from missing or null type; missing values
		// permit this candidate's keepalive conversion.
		Type *string `json:"type"` // decoded event type
	}
	if err := json.Unmarshal([]byte(strings.Join(dataParts, "\n")), &payload); err != nil || payload.Type == nil {
		return true
	}
	return *payload.Type == "ping"
}
