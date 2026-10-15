package upstream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// client_tools_stream.go restores Responses client tools while rebuilding SSE
// event metadata and frame delimiters.
// Responses tool restoration can suppress an upstream event or expand it into
// several client events. SSE metadata belongs to the resulting frames, so an
// event line needs to agree with the restored payload type. Metadata is held
// until the data line is transformed, and each emitted payload receives its
// own blank-line delimiter.
//
// One goroutine writes the restored stream through a pipe, flushing each
// emitted payload batch so a live source can deliver frames before EOF. The
// restorer owns per-stream tool state and borrows the supplied mapping, while
// the scanner borrows reusable byte storage until transformation ends. Closing
// the returned body closes the pipe reader and source; worker exit also closes
// the source and reports read or transformation failures through the pipe.

// DefaultSSELineLimit names the five-hundred-MiB scanner limit used by
// upstream SSE consumers unless a caller supplies another value.
const DefaultSSELineLimit = 500 * 1024 * 1024

// responsesClientToolStreamBody exposes transformed pipe reads and owns
// closure of the source.
type responsesClientToolStreamBody struct {
	*io.PipeReader // transformed SSE byte reader
	// source is closed by both body.Close and worker cleanup. Its Close method
	// needs to tolerate repeated, potentially concurrent closes and interrupt its
	// reads when closure is used to stop transformation.
	source io.Closer // owned upstream response closer
}

// Close releases the transformed reader before closing the upstream source.
// That order wakes a worker blocked on pipe output before source.Close can
// wait for upstream I/O; source-read interruption still depends on the source's
// close behavior. io.PipeReader.Close returns nil, leaving the source close
// error as this method's result.
//
// Worker cleanup also closes the source, so its closer needs to tolerate
// repeated, potentially concurrent calls. Close returns after those two close
// operations and does not join the transformation worker.
func (b *responsesClientToolStreamBody) Close() error {
	// A transformer can be blocked while flushing pipe output. Closing the
	// reader first wakes that writer before source closure potentially waits on I/O.
	readerErr := b.PipeReader.Close()
	sourceErr := b.source.Close()
	if readerErr != nil {
		return readerErr
	}
	return sourceErr
}

// NewResponsesClientToolStreamBody starts a pipe-backed transformation of a
// nonnil source using the supplied tool mapping. Close the result when reading
// ends, and keep mapping maps stable until the worker finishes.
//
// Nonpositive maxLineSize supplies a five-hundred-MiB scanner maximum.
// Scanner.Buffer also uses a sixty-four-KiB initial buffer, so smaller
// positive maxima are not strict line-length limits. Closing the result wakes
// a blocked pipe writer and delegates source-read interruption to
// source.Close.
func NewResponsesClientToolStreamBody(
	source io.ReadCloser,
	mapping bridge.ResponsesClientToolMapping,
	maxLineSize int,
) io.ReadCloser {
	reader, writer := io.Pipe()
	body := &responsesClientToolStreamBody{PipeReader: reader, source: source}
	// The pipe lets a live response be consumed while restoration advances in
	// one worker. Reader closure wakes blocked writes; worker cleanup closes the source.
	go transformResponsesClientToolStream(source, writer, mapping, maxLineSize)
	return body
}

// transformResponsesClientToolStream writes restored SSE frames and closes
// destination with any scan, restoration, or write error. It owns source
// cleanup and uses one restorer for the entire stream; source closure must
// tolerate the body wrapper closing it too.
//
// Metadata preceding each data line is attached to its first emitted payload,
// and later payloads receive an event label only when that source frame had
// one. Suppressed payloads discard the pending metadata. Each data line
// becomes its own output frames; fields after its last data line are discarded
// at a following blank separator but flushed when the source ends there
// instead. Valid JSON enters the tool restorer, while other data follows
// ordinary framing. Payload batches and metadata-only frames flush before
// further source reads, with final pending fields flushed before a scanner
// failure is reported.
func transformResponsesClientToolStream(
	source io.ReadCloser,
	destination *io.PipeWriter,
	mapping bridge.ResponsesClientToolMapping,
	maxLineSize int,
) {
	// Every worker exit releases the upstream response, including reader-close
	// and restoration failures. Cleanup errors do not replace the pipe result
	// already selected for the downstream reader.
	defer func() { _ = source.Close() }()
	if maxLineSize <= 0 {
		maxLineSize = 500 * 1024 * 1024
	}

	scanner := bufio.NewScanner(source)
	// Keep the borrowed scanner storage until scanning has ended. Scanner.Buffer
	// uses the initial buffer even when maxLineSize is smaller, so that value
	// alone does not establish a smaller bound on accepted line length.
	scanBuf := httpclient.GetSSEScannerBuf64K()
	defer httpclient.PutSSEScannerBuf64K(scanBuf)
	scanner.Buffer(scanBuf[:0], maxLineSize)
	documents := wire.NewSSEJSONDocumentScanner(scanner)
	restorer := bridge.NewResponsesClientToolStreamRestorer(mapping)
	buffered := bufio.NewWriterSize(destination, 4*1024)
	pendingFields := make([]string, 0, 2)
	frameHadEventField := false
	frameEmitted := false

	writeLine := func(line string) error {
		if _, err := buffered.WriteString(line); err != nil {
			return err
		}
		return buffered.WriteByte('\n')
	}
	// Restoration can change a data event type. Rewrite its pending event
	// labels to that emitted type while retaining other fields on the first payload.
	writePendingFields := func(payload []byte, includeNonEvent bool) error {
		eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		for _, field := range pendingFields {
			if _, isEvent := wire.ExtractSSEEventLine(field); isEvent {
				if eventType != "" {
					if err := writeLine("event: " + eventType); err != nil {
						return err
					}
				} else if err := writeLine(field); err != nil {
					return err
				}
				continue
			}
			if includeNonEvent {
				if err := writeLine(field); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// One input event can become several client events. Give each its own
	// delimiter, retain source metadata on the first, and synthesize later labels
	// when the original frame supplied an event field.
	writePayloads := func(payloads [][]byte) error {
		for index, payload := range payloads {
			if index == 0 {
				if err := writePendingFields(payload, true); err != nil {
					return err
				}
			} else if frameHadEventField {
				eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
				if eventType != "" {
					if err := writeLine("event: " + eventType); err != nil {
						return err
					}
				}
			}
			if err := writeLine("data: " + string(payload)); err != nil {
				return err
			}
			if err := writeLine(""); err != nil {
				return err
			}
		}
		// A live response can remain open after producing a complete frame.
		// Flush its transformed payloads now so the caller receives them before EOF.
		return buffered.Flush()
	}

	for documents.Scan() {
		line := documents.Text()
		data, isData := wire.ExtractSSEDataLine(line)
		if isData {
			payload := []byte(data)
			payloads := [][]byte{payload}
			// Non-JSON data such as the completion sentinel remains forwardable.
			// JSON payloads enter the stateful tool restorer and may expand or disappear.
			if json.Valid(payload) {
				var err error
				payloads, _, err = restorer.RestoreEvent(payload)
				if err != nil {
					// RestoreEvent may fail after earlier frames were emitted. Attempt
					// to release pending bytes, then report restoration as the pipe error
					// even when that best-effort flush also fails.
					_ = buffered.Flush()
					_ = destination.CloseWithError(fmt.Errorf("restore Responses client tool event: %w", err))
					return
				}
			}
			if err := writePayloads(payloads); err != nil {
				_ = destination.CloseWithError(err)
				return
			}
			// Suppressed events still consume their source metadata. Reset it
			// here so a later data line cannot inherit fields from an omitted event.
			pendingFields = pendingFields[:0]
			frameHadEventField = false
			frameEmitted = true
			continue
		}

		// Metadata-only frames are emitted at their separator. After a data line
		// has already emitted its own delimiter, this separator instead discards
		// any later pending fields and resets the source-frame state.
		if line == "" {
			if !frameEmitted {
				for _, field := range pendingFields {
					if err := writeLine(field); err != nil {
						_ = destination.CloseWithError(err)
						return
					}
				}
				if len(pendingFields) > 0 {
					if err := writeLine(""); err != nil {
						_ = destination.CloseWithError(err)
						return
					}
					if err := buffered.Flush(); err != nil {
						_ = destination.CloseWithError(err)
						return
					}
				}
			}
			pendingFields = pendingFields[:0]
			frameHadEventField = false
			frameEmitted = false
			continue
		}

		if _, isEvent := wire.ExtractSSEEventLine(line); isEvent {
			frameHadEventField = true
		}
		pendingFields = append(pendingFields, line)
	}

	// A source may end with fields lacking a final separator. Flush those
	// fields before exposing a scan error so already-read tail bytes reach the reader.
	for _, field := range pendingFields {
		if err := writeLine(field); err != nil {
			_ = destination.CloseWithError(err)
			return
		}
	}
	if err := buffered.Flush(); err != nil {
		_ = destination.CloseWithError(err)
		return
	}
	if err := documents.Err(); err != nil {
		_ = destination.CloseWithError(err)
		return
	}
	// A clean scanner end closes the write side with EOF after its buffered
	// tail has been delivered, allowing the downstream reader to finish normally.
	_ = destination.Close()
}
