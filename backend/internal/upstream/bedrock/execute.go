package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// execute.go executes Bedrock attempts and reports Messages output, usage, and
// timing observations.
// Bedrock attempts use a regional Invoke request and return either JSON or AWS
// EventStream adapted to Messages output. The request model remains distinct
// from the client response model, while server payloads supply observed model
// and usage presence, including explicit zero counters.
//
// Execution keeps a lifecycle entry through final response closing. Stream
// observations update usage presence and semantic timing before output
// conversion; buffered output parses metrics before the same accounting
// observations. A downstream buffered write failure records disconnect while
// retaining the completed upstream result. Early failures still receive
// cancellation and elapsed-time fields through the failure defer, using its
// entry timestamp when no later timing result exists.

// Target supplies Bedrock transport, retry, response observation, and
// lifecycle callbacks for one attempt.
type Target struct {
	ProviderID int64          // selected provider audit identifier
	Request    RequestOptions // regional request and authentication settings
	Retry      RetryPolicy    // provider-local HTTP retry settings
	Stream     StreamOptions  // EventStream output and idle policy
	// StartedAt controls duration and semantic-output timing after registration.
	// A zero value starts that timing locally; early failure duration uses the
	// executor entry timestamp.
	StartedAt time.Time // caller request timing origin
	// Enter optionally registers the attempt and returns a nonnil release
	// callback. Release follows final body closing or any failure after
	// registration.
	Enter func() (func(), error) // optional attempt lifecycle registration
	// Accepted optionally records upstream acceptance after status handling. The
	// callback precedes response-body reading and therefore does not establish
	// completed delivery to the client.
	Accepted func() // accepted upstream response notification
	// ReadBody is required for buffered output after an accepted response.
	// Streaming reads use StreamResponse, while a missing buffered reader returns
	// a configuration error.
	ReadBody func(io.Reader) ([]byte, error) // caller-bounded buffered response reader
	// HTTPError optionally interprets statuses at least 400 before acceptance.
	// Nil selects a generic error result, and the executor closes the response in
	// either path.
	// upstream error-response result mapping
	HTTPError func(context.Context, *http.Response) (upstream.AttemptResult, error)
}

// Executor exchanges prepared Bedrock requests and returns independent
// upstream and delivery observations.
type Executor struct{}

// TargetID returns the provider identifier used for Bedrock execution
// diagnostics and observation. A nil target returns zero so diagnostic callers
// can identify an absent execution target safely.
func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String formats the provider identifier for Bedrock target diagnostics. A nil
// target uses provider zero, and request credential, signer, and callback
// values stay outside the resulting label.
func (t *Target) String() string { return fmt.Sprintf("bedrock target provider=%d", t.TargetID()) }

// GoString delegates to String for Go-syntax diagnostic formatting of a
// Bedrock target. A nil receiver uses provider zero, and the label retains the
// audit identifier alone.
func (t *Target) GoString() string { return t.String() }

// Execute sends one prepared Bedrock Messages attempt and returns its upstream
// and client-delivery observations. Callers align Request.Stream with
// input.Stream, supply the transport and selected response callbacks, and
// retain borrowed request bytes through exchange.
//
// The executor closes the final response before releasing optional lifecycle
// registration. Stream callbacks accumulate presence and semantic timing
// before adapter completion; buffered output normalizes invocation metrics
// before observing its message. Client write failures in the buffered path
// mark disconnect and keep the completed upstream result. Failure annotation
// records cancellation and elapsed time even when configuration or
// registration fails before a response exists.
func (Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	// Attach failure timing before target validation so invalid configuration and
	// rejected lifecycle entry still carry elapsed work and cancellation
	// classification in their result.
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
	if !ok || target == nil || target.Request.Do == nil {
		return upstream.AttemptResult{}, errors.New("bedrock execution target is not configured")
	}
	if input.Protocol != protocol.ProtocolAnthropicMessages {
		return upstream.AttemptResult{}, errors.New("unsupported bedrock client protocol")
	}
	if target.Enter != nil {
		done, err := target.Enter()
		if err != nil {
			return upstream.AttemptResult{}, err
		}
		defer done()
	}
	started := target.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	resp, err := ExecuteUpstream(ctx, input.Body, target.Request, target.Retry)
	if err != nil {
		return upstream.AttemptResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if id := resp.Header.Get("x-amzn-requestid"); id != "" && resp.Header.Get("x-request-id") == "" {
		resp.Header.Set("x-request-id", id)
	}
	if resp.StatusCode >= 400 {
		if target.HTTPError != nil {
			return target.HTTPError(ctx, resp)
		}
		return upstream.AttemptResult{RequestID: resp.Header.Get("x-amzn-requestid"), FailureClass: "upstream"}, fmt.Errorf("bedrock upstream status %d", resp.StatusCode)
	}
	if target.Accepted != nil {
		target.Accepted()
	}
	result = upstream.AttemptResult{RequestID: resp.Header.Get("x-amzn-requestid"), UpstreamHeaders: resp.Header, Model: input.ResponseModel, UpstreamModel: target.Request.ModelID, Stream: input.Stream}

	if input.Stream {
		options := target.Stream
		priorObserve := options.Observe
		// Chain caller observation with attempt accounting so explicit zero usage
		// and semantic output are retained even when a later EventStream read or
		// client write fails.
		options.Observe = func(observation anthropic.Observation) {
			if priorObserve != nil {
				priorObserve(observation)
			}
			if result.UpstreamResponseModel == "" {
				result.UpstreamResponseModel = observation.Model
			}
			result.HasUsage = result.HasUsage || observation.HasUsage
			result.Served = result.Served || observation.Semantic
			if observation.Semantic && result.FirstSemanticOutput == nil {
				elapsed := time.Since(started)
				result.FirstSemanticOutput = &elapsed
			}
		}
		stream, streamErr := StreamResponse(ctx, resp, upstream.NewOutputContext(sink), options, started, input.ResponseModel)
		if stream != nil {
			if stream.Usage != nil {
				result.Usage = *stream.Usage
			}
			result.FirstTokenMs = stream.FirstTokenMs
			result.ClientDisconnect = stream.ClientDisconnect
		}
		result.Duration = time.Since(started)
		result.Cancelled = errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded)
		if streamErr != nil {
			result.FailureClass = "upstream"
		}
		return result, streamErr
	}
	if target.ReadBody == nil {
		return result, errors.New("bedrock response reader is not configured")
	}
	body, err := target.ReadBody(resp.Body)
	if err != nil {
		return result, err
	}
	body = TransformBedrockInvocationMetrics(body)
	usage := anthropic.ParseClaudeUsageFromResponseBody(body)
	if usage != nil {
		result.Usage = *usage
	}
	observation := anthropic.ObserveMessage(string(body))
	result.UpstreamResponseModel = observation.Model
	result.HasUsage = observation.HasUsage
	result.Served = observation.Semantic
	if result.Served {
		elapsed := time.Since(started)
		result.FirstSemanticOutput = &elapsed
	}
	head := http.Header{"Content-Type": []string{"application/json"}}
	if id := resp.Header.Get("x-amzn-requestid"); id != "" {
		head.Set("x-request-id", id)
	}
	// Keep the completed upstream usage/result when Begin or Emit fails because
	// delivery failure and observed server work are separate inputs to the caller
	// completion decision.
	if err := sink.Begin(upstream.OutputHead{Status: resp.StatusCode, Header: head}); err != nil {
		result.ClientDisconnect = true
	} else if err := sink.Emit(upstream.OutputEvent{Data: body, Semantic: observation.Semantic, CommitForRetry: true, Terminal: true}); err != nil {
		result.ClientDisconnect = true
	}
	result.Duration = time.Since(started)
	return result, nil
}
