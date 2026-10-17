package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolwire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// execute.go executes Messages attempts and combines response usage, output,
// and timing observations.
// A Messages attempt reports usage presence, semantic output, and elapsed data
// arrival separately. Explicit zero-token usage is observable, and an upstream
// model declaration is recorded before downstream rewriting can restore the
// client's model name.
//
// Executor holds an optional activity entry through response processing and
// closes the selected response body before releasing that entry. Response
// inspection runs before acceptance and can end the attempt before output
// starts. Standard and passthrough readers supply observations and partial
// usage through callbacks and returned state; failures retain those
// observations along with cancellation and timing information.

// Target supplies one provider's Messages exchange, response rules, and ordered
// observation hooks. Callers keep its callbacks and referenced options usable
// through the synchronous attempt.
type Target struct {
	ProviderID int64           // selected provider record ID
	Model      string          // selected upstream model name
	Exchange   ExchangeOptions // provider exchange preparation options
	Response   ResponseOptions // response read and presentation options
	// StartedAt anchors response arrival and semantic timing. A zero value
	// selects the time after Enter succeeds, while early failures use Execute's
	// entry time.
	StartedAt time.Time
	// Passthrough selects passthrough exchange and response readers together.
	// MimicClaudeCode is passed to the standard stream reader; the selected
	// Exchange builder separately controls request preparation for this
	// attempt.
	Passthrough, MimicClaudeCode bool // Passthrough: raw response mode; MimicClaudeCode: CLI request mode
	// Enter optionally acquires an activity entry before exchange. Success
	// returns a callable release that Execute defers until after the selected
	// response body is closed.
	Enter func() (func(), error)
	// BeforeResponse inspects the selected response and wire bytes before
	// acceptance. A true stop or error ends processing before readers run;
	// Execute still closes the body.
	BeforeResponse func(context.Context, *http.Response, []byte) (stop bool, err error)
	// BeforeStream runs synchronously after acceptance and output-context
	// construction, immediately before the selected streaming reader. It is
	// unused for buffered response paths.
	BeforeStream func()
	// OnStream receives the selected reader's result and error before Execute
	// merges returned stream state. Partial results can contain usage and
	// disconnect observations after a reader failure.
	OnStream func(*StreamResult, error)
	// OnWireBody receives the exchange's recorded wire bytes before its error
	// is handled. Early build failures can leave the supplied attempt body as
	// that recorded value.
	OnWireBody func([]byte)
	// Accepted runs after exchange returns a usable response and BeforeResponse
	// permits processing, before response reading begins. A configured hook
	// also runs on passthrough targets; callers choose whether to bind it.
	Accepted func()
}

// Executor runs a prepared Messages provider attempt through synchronous output
// and response observations.
type Executor struct{}

// TargetID returns the selected provider identifier, or zero for a nil target.
// Callers can identify an unconfigured target while using the same method for
// configured attempt records.
func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String returns a diagnostic description containing the target's provider ID.
func (t *Target) String() string { return fmt.Sprintf("anthropic target provider=%d", t.TargetID()) }

// GoString returns the same provider description for Go-syntax diagnostic
// formatting.
func (t *Target) GoString() string { return t.String() }

// Execute runs one Anthropic Messages attempt through the target's exchange and
// response reader. It validates target and protocol, holds an optional Enter
// release, reports constructed wire bytes, and closes a selected response body
// before returning. BeforeResponse may stop processing before Accepted and
// output.
//
// Response observations retain usage presence even for zero counts, the first
// upstream model, semantic output, and timing separately from client writes.
// Readers can return partial usage after stream failure or disconnection; that
// state is merged with callback observations. A missing usage speed can use the
// request's fast marker. Failures retain cancellation classification and
// duration, including failures that occur before any usage is observed.
func (Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.Finish(ctx, failure) }()
	// Failure can occur before a response supplies usage or a start timestamp.
	// Classify cancellation and fill missing failure timing at return so these
	// early attempts remain observable.
	enteredAt := time.Now()
	defer func() {
		if failure != nil {
			result.Cancelled = errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
			if result.FailureClass == "" {
				result.FailureClass = "upstream"
			}
			if result.Duration == 0 {
				result.Duration = time.Since(enteredAt)
			}
		}
	}()
	target, ok := input.Target.(*Target)
	if !ok || target == nil {
		return upstream.AttemptResult{}, errors.New("anthropic execution target is not configured")
	}
	if input.Protocol != protocol.ProtocolAnthropicMessages {
		return upstream.AttemptResult{}, errors.New("unsupported anthropic client protocol")
	}
	if target.Enter != nil {
		done, err := target.Enter()
		if err != nil {
			return upstream.AttemptResult{}, err
		}
		// Hold the acquired activity entry through exchange and response
		// processing. Later response cleanup is deferred after this release, so
		// its body closes before the entry is returned.
		defer done()
	}
	started := target.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	var resp *http.Response
	var wire []byte
	var err error
	if target.Passthrough {
		resp, wire, err = ExchangePassthrough(ctx, input.Body, target.Exchange)
	} else {
		resp, wire, err = Exchange(ctx, input.Body, target.Exchange)
	}
	if target.OnWireBody != nil {
		target.OnWireBody(wire)
	}
	if err != nil {
		return upstream.AttemptResult{}, err
	}
	if resp == nil || resp.Body == nil {
		return upstream.AttemptResult{}, errors.New("upstream request failed: empty response")
	}
	// The executor owns the selected response after exchange handoff. Close it
	// on every following return, including response interception and reader
	// failure, to release its transport resources.
	defer func() { _ = resp.Body.Close() }()
	if target.BeforeResponse != nil {
		stop, err := target.BeforeResponse(ctx, resp, wire)
		if stop || err != nil {
			return upstream.AttemptResult{}, err
		}
	}
	if target.Accepted != nil {
		target.Accepted()
	}
	result = upstream.AttemptResult{RequestID: resp.Header.Get("x-request-id"), UpstreamHeaders: resp.Header, Model: input.ResponseModel, UpstreamModel: target.Model, Stream: input.Stream}

	response := target.Response
	priorObserve := response.Observe
	// Retain the existing observer while accumulating attempt evidence from raw
	// upstream payloads. Semantic timing records observation independently of
	// the sink's eventual write result.
	response.Observe = func(observation protocolwire.Observation) {
		if priorObserve != nil {
			priorObserve(observation)
		}
		if result.UpstreamResponseModel == "" {
			result.UpstreamResponseModel = observation.Model
		}
		result.HasUsage = result.HasUsage || observation.HasUsage
		result.Served = result.Served || observation.Semantic
	}
	priorState := response.ObserveState
	// Readers update usage incrementally, including after a downstream
	// disconnect. Copy their current counters into the attempt result while
	// retaining the existing state-observation callback.
	response.ObserveState = func(usage *upstream.TokenUsage) {
		if priorState != nil {
			priorState(usage)
		}
		if usage != nil {
			result.Usage = *usage
		}
	}
	output := upstream.NewOutputContext(sink)
	if input.Stream {
		if target.BeforeStream != nil {
			target.BeforeStream()
		}
		var stream *StreamResult
		if target.Passthrough {
			stream, err = StreamResponsePassthrough(ctx, resp, output, response.StreamOptions, started, target.Model)
		} else {
			stream, err = StreamResponse(ctx, resp, output, response.StreamOptions, started, input.ResponseModel, target.Model, target.MimicClaudeCode)
		}
		if stages := timing.StagesFromContext(ctx); stages != nil {
			stages.FinishAttempt(stages.Attempt())
		}
		if target.OnStream != nil {
			target.OnStream(stream, err)
		}
		if stream != nil {
			if stream.Usage != nil {
				result.Usage = *stream.Usage
			}

			result.ClientDisconnect = stream.ClientDisconnect
		}
	} else {
		var usage *upstream.TokenUsage
		if target.Passthrough {
			usage, err = NonStreamResponsePassthrough(ctx, resp, output, response)
		} else {
			usage, err = NonStreamResponse(ctx, resp, output, response, input.ResponseModel, target.Model)
		}
		if usage != nil {
			result.Usage = *usage
		}
	}
	// A response can omit speed even when the sent request selected fast
	// processing. Use that wire marker only for an empty observed speed,
	// leaving reported response values authoritative.
	if strings.TrimSpace(result.Usage.Speed) == "" && strings.EqualFold(strings.TrimSpace(gjson.GetBytes(wire, "speed").String()), "fast") {
		result.Usage.Speed = "fast"
	}
	result.Duration = time.Since(started)
	result.Cancelled = errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if err != nil {
		result.FailureClass = "upstream"
	}
	return result, err
}
