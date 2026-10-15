package text

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// executor.go runs Messages and Gemini text sessions while capturing their
// latest forwarding evidence. Observation wraps request-local operations to
// retain selected provider, route and invocation count while the adapter
// controls output and completion.
//
// The latest returned error can come from a later selection or rejected
// operation after an earlier attempt produced result data. Keeping that result
// separate lets the HTTP entry inspect partial work alongside the terminal
// error. Attempts counts Forward invocations, including a local stop before any
// network dispatch, rather than counting HTTP exchanges. Entry kind selects
// captured Messages or Gemini options, and request hints/routing are bound
// before the runtime opens its session.

// ErrExecutionRejected marks an adapter operation that stopped execution after
// preflight; its output adapter owns the specific client-facing rejection response.
var ErrExecutionRejected = errors.New("text execution rejected by request operation")

// MessageRuntime opens request-local adapter operations using dependencies
// captured at construction.
type MessageRuntime interface {
	// Open creates usable request-local MessagePorts or returns an initialization
	// error. The caller supplies request and output references appropriate to the
	// runtime; those adapters own output, acquired resources, lifecycle and
	// completion handling.
	Open(context.Context, execution.Request, upstream.OutputSink) (MessagePorts, error)
}

// MessagesExecutor applies captured entry budgets to a runtime-opened Messages
// request session.
type MessagesExecutor struct {
	runtime MessageRuntime // shared request-session runtime binding
	// messages and gemini are captured option values. Execute selects gemini for
	// Gemini kind or AlternateBudget, then copies request session-affinity evidence
	// into that execution's HasBoundSession option.
	messages, gemini MessageOptions // Messages request options and Gemini request options
}

// messageExecutionObservation retains selected and forwarded evidence around
// one serial Messages session.
type messageExecutionObservation struct {
	// selected supplies provider and plan metadata when Forward updates result.
	// err can subsequently reflect selection or rejection, while result retains
	// the last forwarded attempt and accumulated invocation count.
	selected     Selection                 // most recent successful selection
	MessagePorts                           // delegated request operations for this session
	result       execution.ExecutionResult // most recent forwarded execution evidence
	err          error                     // latest observed execution error
}

// NewMessagesExecutor captures runtime and the Messages/Gemini option values
// for later requests. It starts no request work and validates no runtime
// binding, so callers supply a usable runtime before Execute.
func NewMessagesExecutor(runtime MessageRuntime, messages, gemini MessageOptions) *MessagesExecutor {
	return &MessagesExecutor{runtime: runtime, messages: messages, gemini: gemini}
}

// Execute opens one Messages session and returns its most recently forwarded
// result plus the latest observed error. Callers provide a nonnil context and
// usable runtime; request hints and routing are bound before Open.
//
// Gemini kind or AlternateBudget chooses the Gemini option value, then request
// session-affinity evidence replaces HasBoundSession for this execution. The
// observation wrapper counts Forward invocations and retains attempt references
// rather than cloning their mutable contents. A later selection/rejection error
// can accompany the preceding result, so callers inspect both when handling
// partial work after the loop ends.
func (e *MessagesExecutor) Execute(ctx context.Context, in execution.Request, sink upstream.OutputSink) (execution.ExecutionResult, error) {
	ctx = requeststate.WithExecutionHints(ctx, in.Hints)
	ctx = requeststate.WithRoutingState(ctx, in.Routing)
	session, err := e.runtime.Open(ctx, in, sink)
	if err != nil {
		return execution.ExecutionResult{}, err
	}
	options := e.messages
	if in.Text.Kind == execution.TextGeminiMessages || in.Text.AlternateBudget {
		options = e.gemini
	}
	options.HasBoundSession = in.Text.HasBoundSession
	observed := &messageExecutionObservation{MessagePorts: session}
	RunMessages(options, observed)
	return observed.result, observed.err
}

// PrepareAttempt delegates preparation and records ErrExecutionRejected when it
// declines execution. The adapter retains responsibility for its specific
// output, and true leaves error state to later selection or forwarding
// observations.
func (o *messageExecutionObservation) PrepareAttempt() bool {
	ok := o.MessagePorts.PrepareAttempt()
	if !ok {
		o.err = ErrExecutionRejected
	}
	return ok
}

// Select delegates selection and records its error or a successful captured
// candidate. Success clears the preceding observation error; a failed selection
// leaves the previous forwarded result available to the caller.
func (o *messageExecutionObservation) Select(excluded map[int64]struct{}) (Selection, error) {
	s, err := o.MessagePorts.Select(excluded)
	if err != nil {
		o.err = err
	} else {
		o.selected = s
		o.err = nil
	}
	return s, err
}

// Acquire delegates attempt admission and records ErrExecutionRejected on
// false. It returns the adapter's decision unchanged, leaving resource
// ownership and any specific client error with that adapter.
func (o *messageExecutionObservation) Acquire() bool {
	ok := o.MessagePorts.Acquire()
	if !ok {
		o.err = ErrExecutionRejected
	}
	return ok
}

// Forward delegates one attempt and captures provider, plan, result and
// invocation count before returning its outcome. Referenced result fields
// remain assigned by value; Stop with no error records ErrExecutionRejected for
// Execute's caller.
func (o *messageExecutionObservation) Forward(state AttemptState) Outcome {
	out := o.MessagePorts.Forward(state)
	o.result.Provider = o.selected.Provider
	o.result.Plan = o.selected.Plan
	o.result.PlanProvided = o.selected.PlanProvided
	o.result.Attempts++
	o.result.Attempt = out.Attempt
	o.err = out.Err
	if out.Stop && o.err == nil {
		o.err = ErrExecutionRejected
	}
	return out
}

// Canceled records the current adapter context's error and then delegates
// cancellation handling. The context read defines the returned error value; the
// adapter owns final output or cleanup for that stopped sequence.
func (o *messageExecutionObservation) Canceled() {
	o.err = o.Context().Err()
	o.MessagePorts.Canceled()
}
