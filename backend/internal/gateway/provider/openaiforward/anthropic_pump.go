package openaiforward

import (
	"bufio"
	"errors"
	"io"
	"time"
)

// Anthropic conversion readers wait for scanner lines while a separate
// goroutine can block in the upstream body. A timer measures the consumer's
// interval between accepted events, beginning at construction and continuing
// while each event is processed; disabled intervals leave the consumer waiting
// on scanner completion alone.
//
// The event channel carries both line data and scanner failures, and closure
// marks scanner completion after queued events. Stop releases a goroutine blocked
// publishing an event, but body closure remains the caller's responsibility
// when Scan is blocked on input. Consumers serialize pump methods and stop
// once, retaining reader ownership until the scanning goroutine can finish.

// ErrAnthropicStreamIdle identifies consumer wait expiry so conversion readers
// can close the upstream body and return their applicable timeout result.
var ErrAnthropicStreamIdle = errors.New("stream data interval timeout")

// anthropicNativeLineEvent contains one SSE line or a scanner error.
// Normal end of input closes the event channel, which Next reports as io.EOF.
type anthropicNativeLineEvent struct {
	line string // scanned SSE line
	err  error  // scanner failure evidence
}

// AnthropicLinePump reads scanner lines in a goroutine and bounds waits with an interval timer.
// A nonpositive interval disables the timer.
type AnthropicLinePump struct {
	// events and done coordinate scanner publication with Stop; timer and
	// interval govern consumer waits, with a nil timer disabling expiry.
	events   chan anthropicNativeLineEvent // queued scanner event channel
	done     chan struct{}                 // scanner publication stop signal
	timer    *time.Timer                   // optional consumer expiry timer
	interval time.Duration                 // time between consumed events
}

// NewAnthropicLinePump starts scanner publication and returns its line
// consumer. A positive interval starts the wait timer immediately; nonpositive
// intervals disable expiry, and callers serialize pump methods and call Stop
// once. The scanner is borrowed until its goroutine finishes.
//
// Queued publication can race with Stop, so the done signal lets a blocked
// channel send finish without waiting for another consumer read. Stop cannot
// interrupt scanner input itself; the caller closes the underlying body when
// abandoning reads. Normal scanner completion closes events, while scanner
// failures are published as events before that closure.
func NewAnthropicLinePump(scanner *bufio.Scanner, interval time.Duration) *AnthropicLinePump {
	p := &AnthropicLinePump{
		events:   make(chan anthropicNativeLineEvent, 16),
		done:     make(chan struct{}),
		interval: interval,
	}
	if interval > 0 {
		p.timer = time.NewTimer(interval)
	}
	// Publication can block after the sixteen-slot queue fills; select the stop
	// signal on each send so abandoned consumers can release that publisher.
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

// Next returns a scanned line, scanner error, EOF, or ErrAnthropicStreamIdle
// from the next ready source. The interval begins at construction and resets
// when a queued event is consumed, including an error event. It continues while
// the caller processes the preceding line.
//
// Channel closure reports EOF; a positive timer reports idle expiry when
// selected. If a queued event and timer are both ready, selection provides no
// priority between them. Call Next serially while the pump is active, then stop
// it and close the borrowed reader when processing ends.
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

// resetTimer restarts a configured interval after consumer event receipt. A nil
// timer is ignored, and an already-fired timer is drained when available before
// Reset so a previous expiry does not remain queued.
func (p *AnthropicLinePump) resetTimer() {
	if p.timer == nil {
		return
	}
	// Drain an available expired notification before resetting the consumer
	// interval; otherwise the next wait could observe expiry from the preceding
	// interval.
	if !p.timer.Stop() {
		select {
		case <-p.timer.C:
		default:
		}
	}
	p.timer.Reset(p.interval)
}

// Stop closes the scanner publication signal and stops a configured interval
// timer. Call it once after serial consumption; closing the borrowed reader
// separately releases a goroutine blocked inside Scan, and Stop does not wait
// for that goroutine.
func (p *AnthropicLinePump) Stop() {
	close(p.done)
	if p.timer != nil {
		// Stop may find an expiry already queued; discard it while ending the pump
		// lifetime so no consumer retains an obsolete timer notification.
		if !p.timer.Stop() {
			select {
			case <-p.timer.C:
			default:
			}
		}
	}
}
