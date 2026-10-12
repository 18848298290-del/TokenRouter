package text

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// ErrExecutionRejected indicates that a request operation rejected execution after preflight.
// The output adapter writes the specific HTTP error.
var ErrExecutionRejected = errors.New("text execution rejected by request operation")

// MessageRuntime binds execution dependencies at construction; Open creates request-local state.
// The returned session implements MessagePorts for the RunMessages provider loop.
type MessageRuntime interface {
	Open(context.Context, execution.Request, upstream.OutputSink) (MessagePorts, error)
}
type MessagesExecutor struct {
	runtime          MessageRuntime
	messages, gemini MessageOptions
}

type messageExecutionObservation struct {
	selected Selection
	MessagePorts
	result execution.ExecutionResult
	err    error
}

func NewMessagesExecutor(runtime MessageRuntime, messages, gemini MessageOptions) *MessagesExecutor {
	return &MessagesExecutor{runtime: runtime, messages: messages, gemini: gemini}
}

// Execute runs a text request and returns the observed attempt result to the HTTP entry.
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

func (o *messageExecutionObservation) PrepareAttempt() bool {
	ok := o.MessagePorts.PrepareAttempt()
	if !ok {
		o.err = ErrExecutionRejected
	}
	return ok
}

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

func (o *messageExecutionObservation) Acquire() bool {
	ok := o.MessagePorts.Acquire()
	if !ok {
		o.err = ErrExecutionRejected
	}
	return ok
}

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

func (o *messageExecutionObservation) Canceled() {
	o.err = o.Context().Err()
	o.MessagePorts.Canceled()
}
