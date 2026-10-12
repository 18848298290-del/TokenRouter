package forward

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Anthropic conversion receives logical SSE frames that can span many short
// lines, so the transport's per-line bound cannot limit accumulated frame
// storage. Blank lines finish frames and reset their budget; every nonblank line
// contributes its bytes and one newline before the parser can buffer it,
// including comments and fields that do not contribute event data.
//
// A normal EOF can finish a frame without its final blank line. A read error
// still permits a buffered valid JSON value to reach conversion for usage and
// terminal observations, while incomplete JSON is discarded. The read error
// remains available through Err. An oversized frame stops further line reads
// and releases buffered parser/frame storage before returning its size error.

// defaultConversionSSEFrameBytes supplies the 500 MiB accumulated frame budget
// when a response adapter provides no positive frame limit.
const defaultConversionSSEFrameBytes = 500 * 1024 * 1024

// ErrConversionSSEFrameTooLarge identifies accumulated-frame overflow so output
// adapters reject incomplete conversion even when earlier events declared a stop.
var ErrConversionSSEFrameTooLarge = errors.New("the upstream SSE frame exceeds the size limit")

// conversionSSEScanner turns response lines into bounded frames for both conversion output modes.
type conversionSSEScanner struct {
	lines Lines // upstream line cursor
	// parser buffers current event fields, while frame retains the last selected
	// result. Scan replaces frame at delivery and clears both after size failure.
	parser openai.OpenAICompatSSEFrameParser // buffered event fields
	frame  openai.OpenAICompatSSEFrame       // selected complete frame
	// finished stops subsequent reads after EOF, reader failure, or frame overflow.
	// An EOF frame can still be delivered by that last Scan before later false results.
	finished bool
	maxBytes int // accumulated frame budget
	// frameBytes counts nonblank line bytes plus their newline separators.
	// Blank delimiters reset the counter without consuming space in the next frame.
	frameBytes int
	err        error // retained terminal failure
}

// newConversionSSEScanner constructs a frame reader using the response's Lines
// and frame limit. Lines must be usable; a nonpositive limit selects 500 MiB,
// independently of the transport reader's per-line bound.
func newConversionSSEScanner(in Response) *conversionSSEScanner {
	limit := in.MaxSSEFrameBytes
	if limit <= 0 {
		limit = defaultConversionSSEFrameBytes
	}
	return &conversionSSEScanner{lines: in.Lines, maxBytes: limit}
}

// Scan selects the next complete frame, returning false after terminal reading
// or size failure. Each nonblank line and newline must fit before parser
// buffering, so short-line accumulation and ignored comment fields share the
// same limit. A blank line resets the budget before frame delivery.
//
// At EOF the parser can finish its remaining frame. On reader failure only a
// valid JSON value is delivered, allowing usage capture while Err retains the
// source failure. Overflow discards parser and frame storage and prevents all
// later reads; callers inspect Err after false to distinguish it from exhaustion.
func (s *conversionSSEScanner) Scan() bool {
	if s.finished {
		return false
	}
	for s.lines.Scan() {
		// Normalize a trailing carriage return before budgeting and parsing.
		// Lines implementations can expose CRLF differently, while SSE delimiters
		// and field text need one normalized representation.
		line := strings.TrimSuffix(s.lines.Text(), "\r")
		if line != "" {
			// Reserve each newline before parser buffering so many individually
			// short lines cannot exceed the response's accumulated frame bound.
			// A line plus newline exactly filling the remaining budget is accepted.
			if len(line) >= s.maxBytes-s.frameBytes {
				s.err = ErrConversionSSEFrameTooLarge
				s.finished = true
				s.parser = openai.OpenAICompatSSEFrameParser{}
				s.frame = openai.OpenAICompatSSEFrame{}
				return false
			}
			s.frameBytes += len(line) + 1
		} else {
			s.frameBytes = 0
		}
		if frame, ok := s.parser.AddLine(line); ok {
			s.frame = frame
			return true
		}
	}
	s.finished = true
	s.err = s.lines.Err()
	frame, ok := s.parser.Finish()
	// A connection failure can follow a complete final JSON value without
	// its blank delimiter. Deliver valid buffered data for usage/stop capture,
	// but leave Err set so the output adapter also observes the failed read.
	if s.err != nil && !json.Valid([]byte(frame.Data)) {
		return false
	}
	s.frame = frame
	return ok
}

// Err returns the retained reader or size failure after scanning stops, or nil
// for ordinary exhaustion. A final valid JSON frame can be delivered alongside
// a reader error, so callers inspect this even after processing that frame.
func (s *conversionSSEScanner) Err() error {
	return s.err
}
