package openaiforward

import (
	"bufio"
	"errors"
	"io"
	"time"
)

// ErrAnthropicStreamIdle indicates that the line pump's interval timer expired.
var ErrAnthropicStreamIdle = errors.New("stream data interval timeout")

// anthropicNativeLineEvent contains one SSE line or a scanner error.
// Normal end of input closes the event channel, which Next reports as io.EOF.
type anthropicNativeLineEvent struct {
	line string
	err  error
}

// AnthropicLinePump reads scanner lines in a goroutine and bounds waits with an interval timer.
// A nonpositive interval disables the timer.
type AnthropicLinePump struct {
	events   chan anthropicNativeLineEvent
	done     chan struct{}
	timer    *time.Timer
	interval time.Duration
}

// NewAnthropicLinePump starts the reader goroutine. The caller must call Stop once and close the underlying reader.
func NewAnthropicLinePump(scanner *bufio.Scanner, interval time.Duration) *AnthropicLinePump {
	p := &AnthropicLinePump{
		events:   make(chan anthropicNativeLineEvent, 16),
		done:     make(chan struct{}),
		interval: interval,
	}
	if interval > 0 {
		p.timer = time.NewTimer(interval)
	}
	go func() {
		defer close(p.events)
		for scanner.Scan() {
			select {
			case p.events <- anthropicNativeLineEvent{line: scanner.Text()}:
			case <-p.done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case p.events <- anthropicNativeLineEvent{err: err}:
			case <-p.done:
			}
		}
	}()
	return p
}

// Next waits for a line, scanner error, EOF, or ErrAnthropicStreamIdle.
// The timer starts at construction and resets when Next consumes an event;
// it continues running while the caller processes that event.
func (p *AnthropicLinePump) Next() (string, error) {
	var timeoutCh <-chan time.Time
	if p.timer != nil {
		timeoutCh = p.timer.C
	}
	select {
	case ev, ok := <-p.events:
		if !ok {
			return "", io.EOF
		}
		p.resetTimer()
		return ev.line, ev.err
	case <-timeoutCh:
		return "", ErrAnthropicStreamIdle
	}
}

// resetTimer restarts the interval timer after an event is consumed.
func (p *AnthropicLinePump) resetTimer() {
	if p.timer == nil {
		return
	}
	if !p.timer.Stop() {
		select {
		case <-p.timer.C:
		default:
		}
	}
	p.timer.Reset(p.interval)
}

// Stop signals the pump to exit and stops its timer. Call it once.
// The caller must close the underlying reader to unblock a goroutine inside scanner.Scan.
func (p *AnthropicLinePump) Stop() {
	close(p.done)
	if p.timer != nil {
		if !p.timer.Stop() {
			select {
			case <-p.timer.C:
			default:
			}
		}
	}
}
