package forward

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const defaultConversionSSEFrameBytes = 500 * 1024 * 1024

// ErrConversionSSEFrameTooLarge indicates that accumulated frame bytes exceed the conversion limit.
var ErrConversionSSEFrameTooLarge = errors.New("the upstream SSE frame exceeds the size limit")

// conversionSSEScanner separates events on blank lines, joins data lines, and accepts frames without an event field.
type conversionSSEScanner struct {
	lines      Lines
	parser     openai.OpenAICompatSSEFrameParser
	frame      openai.OpenAICompatSSEFrame
	finished   bool
	maxBytes   int
	frameBytes int
	err        error
}

func newConversionSSEScanner(in Response) *conversionSSEScanner {
	limit := in.MaxSSEFrameBytes
	if limit <= 0 {
		limit = defaultConversionSSEFrameBytes
	}
	return &conversionSSEScanner{lines: in.Lines, maxBytes: limit}
}

func (s *conversionSSEScanner) Scan() bool {
	if s.finished {
		return false
	}
	for s.lines.Scan() {
		line := strings.TrimSuffix(s.lines.Text(), "\r")
		if line != "" {
			// Count complete lines and newlines before buffering; short data lines and comments share the frame budget.
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
	// A connection error may follow complete JSON. Deliver that frame for usage capture while Err retains the read failure.
	if s.err != nil && !json.Valid([]byte(frame.Data)) {
		return false
	}
	s.frame = frame
	return ok
}

func (s *conversionSSEScanner) Err() error {
	return s.err
}
