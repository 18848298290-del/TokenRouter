package ws

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// Passthrough needs a first-output budget while an accepted request is waiting,
// a separate silence budget after qualifying output, and no upstream deadline
// between turns. Terminal labels end the active deadline, while the configured
// output-label predicate chooses when that first budget becomes the active-read
// budget.
//
// DeadlineConn publishes armed state under a mutex and coalesces changes onto a
// notification channel watched by the outstanding read. Each arm advances a
// generation; a failed request write can disarm its own generation without
// clearing a newer request's state. The read controller recomputes remaining
// time from stored start time, checks the latest saved deadline when its timer
// fires, and joins its canceled inner reader before returning a typed timeout
// or caller cancellation.

const (
	// These phases distinguish the pre-output wait from active-turn silence so
	// ReadFrame can return the timeout type used by passthrough failure handling.
	deadlinePhaseFirstSemantic deadlinePhase = iota + 1 // waiting for qualifying output
	deadlinePhaseActiveRead                             // waiting for active-turn activity

	// TextFrame is the WebSocket text message number used by frame filters and
	// upstream event classification.
	TextFrame = 1
)

// These sentinels identify first-output and active-read expiry in error chains
// so passthrough can select provider failover or client reconnect handling.
var (
	// ErrFirstOutputTimeout marks expiry before qualifying upstream output.
	ErrFirstOutputTimeout = errors.New("openai websocket passthrough first output timeout")
	// ErrActiveTurnTimeout marks silence while an upstream turn is active.
	ErrActiveTurnTimeout = errors.New("openai websocket passthrough active turn read timeout")
)

// FrameConn exposes synchronous frame exchange to gateway filters and upstream
// relay adapters.
type FrameConn interface {
	// ReadFrame waits for one frame or transport failure using the supplied
	// context. Adapters can select their own control context; an upstream
	// transport wrapped by DeadlineConn must honor read cancellation because that
	// controller joins its reader.
	ReadFrame(context.Context) (int, []byte, error)
	// WriteFrame synchronously sends the given message type and payload under the
	// supplied context. A nil error confirms write completion to lifecycle
	// callers, which then decide whether terminal delivery opens admission.
	WriteFrame(context.Context, int, []byte) error
	// Close releases the underlying frame transport when its session ends.
	// Implementations retain responsibility for their transport cleanup; callers
	// may close after a failure or after normal relay completion.
	Close() error
}

// deadlinePhase identifies which upstream wait budget produced a timeout.
type deadlinePhase uint8

// Deadline records one armed wait budget and first-output diagnostic metadata.
type Deadline struct {
	// Timeout and StartedAt define the absolute expiry of the currently armed
	// phase. Nonpositive Timeout leaves the deadline disabled, and zero StartedAt
	// is initialized on arming.
	Timeout time.Duration // current phase wait budget
	// ActiveReadTimeout selects the silence budget after qualifying output.
	// Nonpositive values use the connection default, then disable active timing if
	// that default is also nonpositive.
	ActiveReadTimeout time.Duration // subsequent active silence budget
	StartedAt         time.Time     // current budget start time
	RequestModel      string        // first-output diagnostic model label
	ReasoningEffort   string        // first-output diagnostic effort label
	phase             deadlinePhase // timeout classification phase label
}

// FirstOutputTimeoutError retains the expired first-output budget for provider
// failure diagnostics.
type FirstOutputTimeoutError struct {
	Deadline Deadline // expired first-output deadline snapshot
}

// ActiveTurnTimeoutError identifies silence after an upstream turn has produced
// qualifying output.
type ActiveTurnTimeoutError struct{}

// firstOutputDeadlineState is the mutex-protected armed budget and generation
// snapshot.
type firstOutputDeadlineState struct {
	armed bool // upstream wait timer enabled
	// generation advances whenever a deadline is armed. A failed write disarms
	// only its matching generation, leaving any newer published deadline active.
	generation uint64   // latest deadline publication generation
	deadline   Deadline // current wait budget snapshot
}

// DeadlineConn wraps upstream frames with per-turn output and silence budgets.
type DeadlineConn struct {
	closedError error     // injected missing transport error
	inner       FrameConn // wrapped upstream frame transport
	// resolveDeadline is optional; an absent resolver leaves request writes
	// unarmed. Returned nonpositive first-output budgets also skip arming for
	// those request frames.
	resolveDeadline   func(payload []byte) Deadline // request-specific first-output budget resolver
	activeReadTimeout time.Duration                 // default active silence budget

	// mu protects state while reads and writes progress independently.
	// deadlineChanged coalesces publication notifications; the reader reloads
	// state before computing its next timer budget.
	mu              sync.Mutex               // deadline state access lock
	state           firstOutputDeadlineState // published upstream deadline state
	deadlineChanged chan struct{}            // coalesced timer reset notification
}

// Error returns the first-output timeout sentinel text for caller diagnostics.
func (e *FirstOutputTimeoutError) Error() string {
	return ErrFirstOutputTimeout.Error()
}

// Unwrap exposes the first-output sentinel for standard timeout classification.
func (e *FirstOutputTimeoutError) Unwrap() error {
	return ErrFirstOutputTimeout
}

// Error returns the active-turn silence sentinel text for caller diagnostics.
func (e *ActiveTurnTimeoutError) Error() string {
	return ErrActiveTurnTimeout.Error()
}

// Unwrap exposes the active-turn sentinel for standard timeout classification.
func (e *ActiveTurnTimeoutError) Unwrap() error {
	return ErrActiveTurnTimeout
}

// NewDeadlineConn wraps inner with request-resolved first-output and active
// silence budgets. A nil resolver leaves request writes unarmed; nonpositive
// active budgets disable that phase, and callers provide
// cancellation-responsive inner reads.
func NewDeadlineConn(inner FrameConn, activeReadTimeout time.Duration, resolve func([]byte) Deadline, closedError error) *DeadlineConn {
	return &DeadlineConn{inner: inner, activeReadTimeout: activeReadTimeout, resolveDeadline: resolve, closedError: closedError, deadlineChanged: make(chan struct{}, 1)}
}

// ReadFrame waits for one upstream frame while tracking the currently armed
// deadline. Nil context uses Background, and a missing wrapper or inner
// transport returns the configured closure error.
//
// A background inner read progresses alongside timer notifications. Each reset
// calculates remaining time from the saved start rather than granting a new
// full timeout. Expiry rechecks the saved armed state, then caller cancellation
// takes priority when already visible. Timeout and cancellation cancel and join
// the inner reader before returning, so the wrapped transport must honor its
// read context. Successful reads update terminal and active-output timing
// before delivering their bytes.
func (c *DeadlineConn) ReadFrame(ctx context.Context) (int, []byte, error) {
	if c == nil || c.inner == nil {
		return TextFrame, nil, c.connectionClosedError()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// readResult carries one inner frame result to the deadline controller.
	type readResult struct {
		msgType int    // received upstream frame type
		payload []byte // received upstream frame bytes
		err     error  // underlying upstream read failure
	}
	// Run one inner read while timer updates remain observable, and buffer its
	// single result so cancellation can join that reader after stopping it.
	readCtx, cancelRead := context.WithCancel(ctx)
	readResultCh := make(chan readResult, 1)
	go func() {
		msgType, payload, err := c.inner.ReadFrame(readCtx)
		readResultCh <- readResult{msgType: msgType, payload: payload, err: err}
	}()

	var timer *time.Timer
	var timerCh <-chan time.Time
	resetTimer := func() {
		state := c.deadlineState()
		// Drain a fired timer before reusing it, then compute the remaining absolute
		// budget from published state so a notification does not extend the deadline.
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if !state.armed || state.deadline.Timeout <= 0 {
			timerCh = nil
			return
		}
		remaining := max(time.Until(state.deadline.StartedAt.Add(state.deadline.Timeout)), 0)
		if timer == nil {
			timer = time.NewTimer(remaining)
		} else {
			timer.Reset(remaining)
		}
		timerCh = timer.C
	}
	resetTimer()

	defer func() {
		cancelRead()
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case result := <-readResultCh:
			if result.err == nil {
				c.observeUpstreamActivity(result.msgType, result.payload)
			}
			return result.msgType, result.payload, result.err
		case <-c.deadlineChanged:
			resetTimer()
		case <-timerCh:
			// A queued timer expiry can belong to an earlier arm; reload current state
			// before choosing timeout classification or continuing the read.
			state := c.deadlineState()
			if !state.armed || state.deadline.Timeout <= 0 || time.Now().Before(state.deadline.StartedAt.Add(state.deadline.Timeout)) {
				resetTimer()
				continue
			}
			// Return visible caller cancellation ahead of an expired budget, joining the
			// canceled reader so neither path leaves an outstanding transport read.
			if ctx.Err() != nil {
				cancelRead()
				<-readResultCh
				return TextFrame, nil, ctx.Err()
			}
			cancelRead()
			<-readResultCh
			if state.deadline.phase == deadlinePhaseActiveRead {
				return TextFrame, nil, &ActiveTurnTimeoutError{}
			}
			return TextFrame, nil, &FirstOutputTimeoutError{Deadline: state.deadline}
		case <-ctx.Done():
			// Join the inner reader after cancellation so its transport access has ended
			// before the caller handles this session exit.
			cancelRead()
			<-readResultCh
			return TextFrame, nil, ctx.Err()
		}
	}
}

// WriteFrame sends a request frame after arming its first-output budget when
// its trimmed type is response.create. Other frames pass through using the
// current state.
//
// The arm returns a generation identifying this write's published budget. If
// sending fails, disarmDeadline uses that generation to retain any newer budget
// published while the write was in progress. A missing wrapper or inner
// transport returns the configured closure error; successful delivery leaves
// its deadline available for concurrent ReadFrame calls.
func (c *DeadlineConn) WriteFrame(ctx context.Context, msgType int, payload []byte) error {
	if c == nil || c.inner == nil {
		return c.connectionClosedError()
	}
	generation := uint64(0)
	if msgType == TextFrame && strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
		generation = c.armDeadline(payload)
	}
	if err := c.inner.WriteFrame(ctx, msgType, payload); err != nil {
		c.disarmDeadline(generation)
		return err
	}
	return nil
}

// Close delegates cleanup to the wrapped upstream transport when available. A
// nil wrapper or missing inner connection returns nil; transport errors are
// returned for the owning session's cleanup policy.
func (c *DeadlineConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// armDeadline publishes a request-resolved first-output budget and returns its
// generation. A nil wrapper, absent resolver, or nonpositive resolved budget
// returns zero without replacing the current state.
//
// A missing start time uses the current clock. Under the mutex, arming advances
// the generation and publishes all deadline fields together before notifying
// the reader. The returned generation lets a failed write clear its own budget
// while retaining a later publication from another write or output transition.
func (c *DeadlineConn) armDeadline(payload []byte) uint64 {
	if c == nil || c.resolveDeadline == nil {
		return 0
	}
	deadline := c.resolveDeadline(payload)
	if deadline.Timeout <= 0 {
		return 0
	}
	if deadline.StartedAt.IsZero() {
		deadline.StartedAt = time.Now()
	}
	deadline.phase = deadlinePhaseFirstSemantic
	c.mu.Lock()
	c.state.generation++
	generation := c.state.generation
	c.state.armed = true
	c.state.deadline = deadline
	c.mu.Unlock()
	c.notifyDeadlineChanged()
	return generation
}

// observeUpstreamActivity updates deadline state from a successfully read
// frame. Text terminal labels disarm timing before other activity rules run.
//
// Once the active-read phase is armed, every further successful frame restarts
// that silence budget. Before that transition, only a text payload selected by
// StartsSemanticOutput arms active timing. The predicate uses event labels
// rather than validating token content; therefore structural output labels can
// change timeout handling even when no content-based TTFT observation exists.
func (c *DeadlineConn) observeUpstreamActivity(msgType int, payload []byte) {
	if c == nil {
		return
	}
	if msgType == TextFrame && IsTerminalOutput(payload) {
		c.disarmDeadline(0)
		return
	}
	state := c.deadlineState()
	if state.armed && state.deadline.phase == deadlinePhaseActiveRead {
		c.armActiveReadDeadline()
		return
	}
	if msgType == TextFrame && StartsSemanticOutput(payload) {
		c.armActiveReadDeadline()
	}
}

// armActiveReadDeadline replaces the first-output budget with an active-turn
// silence budget. It prefers the saved request's ActiveReadTimeout and then the
// connection default when that value is nonpositive.
//
// A nonpositive effective budget disarms timing. Otherwise publication advances
// the generation, sets a fresh start time and active phase under the mutex,
// then notifies the read controller. This generation change protects active
// timing from an older failed request write trying to clear its first-output
// arm.
func (c *DeadlineConn) armActiveReadDeadline() {
	if c == nil {
		return
	}
	activeReadTimeout := c.deadlineState().deadline.ActiveReadTimeout
	if activeReadTimeout <= 0 {
		activeReadTimeout = c.activeReadTimeout
	}
	if activeReadTimeout <= 0 {
		c.disarmDeadline(0)
		return
	}
	c.mu.Lock()
	c.state.generation++
	c.state.armed = true
	c.state.deadline = Deadline{
		Timeout:           activeReadTimeout,
		ActiveReadTimeout: activeReadTimeout,
		StartedAt:         time.Now(),
		phase:             deadlinePhaseActiveRead,
	}
	c.mu.Unlock()
	c.notifyDeadlineChanged()
}

// disarmDeadline clears an armed deadline matching generation and notifies the
// reader. Zero selects the current deadline without a generation restriction,
// as terminal output and disabled active timing require.
//
// A nonzero generation belongs to a specific request write. When a newer arm
// has already replaced it, the mismatch leaves the newer timer active. State
// selection and clearing occur under one mutex; the notification happens after
// unlocking so the reader can reload the saved state.
func (c *DeadlineConn) disarmDeadline(generation uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.state.armed || (generation != 0 && generation != c.state.generation) {
		c.mu.Unlock()
		return
	}
	c.state.armed = false
	c.mu.Unlock()
	c.notifyDeadlineChanged()
}

// deadlineState copies the published armed flag, generation, and deadline under
// the mutex. A nil wrapper returns empty state; callers use this value snapshot
// to calculate timers without retaining the state lock.
func (c *DeadlineConn) deadlineState() firstOutputDeadlineState {
	if c == nil {
		return firstOutputDeadlineState{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// notifyDeadlineChanged coalesces state changes into the reader's buffered
// notification channel. Nil wrappers, absent channels, and already-pending
// notifications are ignored; readers always reload state after receiving a
// notification.
func (c *DeadlineConn) notifyDeadlineChanged() {
	if c == nil || c.deadlineChanged == nil {
		return
	}
	select {
	case c.deadlineChanged <- struct{}{}:
	default:
	}
}

// StartsSemanticOutput classifies a payload label for switching to active-read
// timing. Terminal labels qualify, empty and listed preamble/item labels do
// not, and remaining labels qualify by delta or output spelling.
//
// This is an event-name rule, not a nonempty-token validator. It accepts any
// label containing .delta and any response.output_text or response.output
// prefix after the exclusions, so callers use it for timeout phases separately
// from their TTFT predicates. JSON field extraction supplies the label even
// when this helper has not validated the full document.
func StartsSemanticOutput(payload []byte) bool {
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch eventType {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	case "", "response.created", "response.in_progress", "response.output_item.added", "response.output_item.done":
		return false
	}
	return strings.Contains(eventType, ".delta") ||
		strings.HasPrefix(eventType, "response.output_text") ||
		strings.HasPrefix(eventType, "response.output")
}

// IsTerminalOutput reports whether the trimmed type names one of six terminal
// labels. It accepts completed, done, failed, incomplete, and both cancellation
// spellings; payload content and complete JSON validity are not checked.
func IsTerminalOutput(payload []byte) bool {
	switch strings.TrimSpace(gjson.GetBytes(payload, "type").String()) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

// connectionClosedError returns the injected closure error when available on a
// nonnil wrapper. A nil wrapper or absent injected error creates the generic
// connection-closed error used by frame callers.
func (c *DeadlineConn) connectionClosedError() error {
	if c != nil && c.closedError != nil {
		return c.closedError
	}
	return errors.New("openai ws connection closed")
}
