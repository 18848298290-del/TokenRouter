package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Compatible streams can concatenate several typed JSON events on one data
// line, causing a normal SSE reader to see one invalid JSON value. Splitting
// succeeds only when every document decodes completely and supplies a usable
// event type; a malformed suffix discards the whole split attempt.
//
// Bounded splitting limits both payload size and document count before the
// scanner expands repaired documents into data lines separated by blank lines.
// The first expanded document retains the surrounding event header; later
// documents receive headers from their own type fields. Unsupported and oversized
// inputs remain physical scanner lines for downstream handling. Pending expanded
// lines are emitted before advancing the underlying scanner, preserving document
// order and keeping the caller responsible for its scanner's lifetime and limits.

// These limits bound one concatenated-event repair's buffered payload and queued
// document expansion before the scanner exposes synthetic SSE lines to its caller.
const (
	MaxConcatenatedJSONDocuments = 16               // maximum typed documents
	MaxConcatenatedJSONBytes     = 16 * 1024 * 1024 // maximum repair bytes
)

// SSEJSONDocumentScanner expands supported concatenated data documents into
// logical SSE lines.
type SSEJSONDocumentScanner struct {
	// scanner is borrowed from the caller. This wrapper consumes its lines
	// and exposes its errors while leaving source ownership and cleanup to that caller.
	scanner *bufio.Scanner
	// pending retains synthetic lines in document order; Scan consumes this
	// queue before asking the underlying scanner for another physical line.
	pending []string
	current string // latest logical line
}

// SplitConcatenatedJSONDocuments returns ordered raw JSON events when payload
// contains at least two complete typed documents within the repair bounds.
// Blank, oversized, already valid single-document, malformed, and untyped inputs
// return nil and false for the caller's ordinary handling.
//
// Every document needs a string type whose trimmed value is nonempty and has
// no CR/LF. A malformed suffix invalidates the whole split attempt.
// RawMessage decoding retains each document's numeric and field representation
// for later forwarding. The document count and trimmed byte size are bounded
// before repaired events enter the scanner's pending output queue.
func SplitConcatenatedJSONDocuments(payload []byte) ([][]byte, bool) {
	payload = bytes.TrimSpace(payload)
	// Bound repair bytes before the decoder allocates document buffers.
	if len(payload) == 0 || len(payload) > MaxConcatenatedJSONBytes || json.Valid(payload) {
		return nil, false
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	documents := make([][]byte, 0, 2)
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err != nil {
			if err == io.EOF && len(documents) > 1 {
				return documents, true
			}
			return nil, false
		}
		raw = bytes.TrimSpace(raw)
		var envelope struct {
			Type string `json:"type"` // decoded event label
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, false
		}
		eventType := strings.TrimSpace(envelope.Type)
		if eventType == "" || strings.ContainsAny(eventType, "\r\n") {
			return nil, false
		}
		if len(documents) == MaxConcatenatedJSONDocuments {
			// Each accepted document creates queued SSE output lines. Stop before
			// another document would exceed the repair bound and enlarge that buffered expansion.
			return nil, false
		}
		documents = append(documents, raw)
	}
}

// NewSSEJSONDocumentScanner borrows scanner for concatenated-event compatibility.
// A nil scanner produces a wrapper whose Scan returns false and Err returns
// nil; the caller retains the source's cleanup and buffering responsibilities.
func NewSSEJSONDocumentScanner(scanner *bufio.Scanner) *SSEJSONDocumentScanner {
	return &SSEJSONDocumentScanner{scanner: scanner}
}

// Scan advances to the next physical or synthetic SSE line, emitting pending
// repaired lines before reading the source scanner. The caller serializes use
// of this nonnil wrapper and reads Text after each true result.
//
// A repair inserts blank separators and later event headers while retaining
// the first document's surrounding header. Failed or oversized split attempts
// return the original physical line. Nil sources and exhausted or errored
// scanners return false; Err exposes the source failure separately from this
// compatibility helper's decision not to split a line.
func (s *SSEJSONDocumentScanner) Scan() bool {
	if len(s.pending) > 0 {
		s.current = s.pending[0]
		s.pending = s.pending[1:]
		return true
	}
	if s.scanner == nil || !s.scanner.Scan() {
		return false
	}

	line := s.scanner.Text()
	data, ok := ExtractSSEDataLine(line)
	if !ok {
		s.current = line
		return true
	}
	if len(data) > MaxConcatenatedJSONBytes {
		// The source scanner has already accepted this physical line under its
		// own limit. Bypass repair to avoid another large compatibility buffer.
		s.current = line
		return true
	}
	documents, repaired := SplitConcatenatedJSONDocuments([]byte(data))
	if !repaired {
		// Unsupported concatenation remains an ordinary physical data line.
		// Let downstream event handling decide its validity instead of
		// delivering a partial document prefix.
		s.current = line
		return true
	}

	expanded := make([]string, 0, len(documents)*3)
	for i, document := range documents {
		// Keep the first event's existing header association. Later documents
		// need their own type-derived header and every expanded document needs
		// a blank frame separator.
		if i > 0 {
			var envelope struct {
				Type string `json:"type"` // decoded event label
			}
			// Splitting already validated this raw document and its type field.
			// Reuse that accepted shape to derive the later header without
			// surfacing another decoding error.
			_ = json.Unmarshal(document, &envelope)
			expanded = append(expanded, "event: "+strings.TrimSpace(envelope.Type))
		}
		expanded = append(expanded, "data: "+string(document), "")
	}
	s.current = expanded[0]
	s.pending = expanded[1:]
	return true
}

// Text returns the logical SSE line selected by the latest successful Scan.
func (s *SSEJSONDocumentScanner) Text() string {
	return s.current
}

// Err returns the borrowed scanner's error, or nil for a missing source.
// A concatenation rejected by the repair rules is still returned as a physical
// line and does not itself create a scanner error.
func (s *SSEJSONDocumentScanner) Err() error {
	if s.scanner == nil {
		return nil
	}
	return s.scanner.Err()
}
