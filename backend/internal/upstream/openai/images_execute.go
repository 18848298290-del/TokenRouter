package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// images_execute.go executes image attempts and records output commitment,
// served images, usage, and delivery failures.
// An image attempt can commit HTTP output or emit progressive image data before
// a completed image is counted. Execution records those observations
// independently, allowing a failed OAuth stream to report partial served output
// while ObservedImages remains zero.
//
// The prepared request owns transport context, and optional admission stays
// held through response reading and cleanup. HTTP failures close the original
// body before replaying redacted bytes into health handling. Successful
// responses choose OAuth/API-key and streaming/buffered readers from the
// prepared mode and content type. A synchronous sink wrapper retains
// commitment, semantic, and write-failure observations across those branches,
// while OAuth response failure policy also considers whether any completed
// image was observed before the error.

// ImagesTarget holds the prepared request, response policy, and image execution
// callbacks.
type ImagesTarget struct {
	ProviderID int64 // selected image provider identifier
	OAuth      bool  // OAuth image conversion selection
	// Model is the selected upstream model, ResponseFormat is the client image
	// encoding choice, and StreamPrefix names the image event family used by
	// OAuth conversion. They describe one prepared attempt across its readers.
	Model, ResponseFormat, StreamPrefix string
	// StartedAt supplies the attempt duration origin; a zero timestamp selects the
	// execution start, and response processing records duration before body
	// cleanup.
	StartedAt time.Time
	// Request contains prepared authentication and owns the transport context;
	// callers redact diagnostics and keep its body usable for this single exchange.
	Request *http.Request        `json:"-"`
	Options ImageResponseOptions // prepared image response configuration
	// Enter optionally admits activity and returns its release callback; Execute
	// defers release through response reading, result observation, and body
	// cleanup.
	Enter           func() (func(), error)
	Do              func(*http.Request) (*http.Response, error) // required HTTP exchange callback
	TransportError  func(error) error                           // required transport failure translator
	ReadErrorBody   func(*http.Response) []byte                 // required HTTP error reader
	RedactErrorBody func([]byte) []byte                         // required captured error redactor
	HTTPError       func(*http.Response, []byte) error          // required HTTP status handler
	ResponseError   func(*http.Response, int, error) error      // required OAuth response policy
}

// ImagesExecutor sends prepared image attempts and records response output and
// usage.
type ImagesExecutor struct{}

// imagesObservedSink records commitment and attempted semantic output through a
// synchronous sink.
type imagesObservedSink struct {
	upstream.OutputSink // wrapped synchronous image output sink
	// committed records accepted headers, retryCommitted records retry-commit
	// event flags, semantic records attempted content, and failed records sink
	// write failure. Emit retains event observations even if delivery fails.
	committed, retryCommitted, semantic, failed bool
}

// TargetID returns the selected image provider identifier or zero for nil
// targets. Diagnostic formatting can therefore describe missing preparation
// before Execute rejects an unavailable request target.
func (t *ImagesTarget) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String formats the image provider identifier while omitting request
// credentials.
func (t *ImagesTarget) String() string {
	return fmt.Sprintf("openai images target provider=%d", t.TargetID())
}

// GoString returns the redacted image target label for Go-syntax formatting.
func (t *ImagesTarget) GoString() string { return t.String() }

// Execute sends one prepared image attempt and reports usage, completed images,
// and output observations. The target supplies required transport/error
// callbacks and response options; Enter is optional, and a zero StartedAt
// selects the current time. The prepared request owns the context used by Do.
//
// HTTP error handling closes the original body before installing a redacted
// replay reader. Successful responses select the appropriate streaming or
// buffered reader, while the sink wrapper records HTTP commitment, retry
// commitment, attempted semantic output, and write failure separately. Served
// can therefore be true for progressive semantic output with no completed
// image. OAuth errors in buffered mode or before a completed streaming image
// invoke ResponseError after those observations are captured.
func (ImagesExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.Finish(ctx, failure) }()
	t, ok := input.Target.(*ImagesTarget)
	if !ok || t == nil || t.Request == nil {
		return result, errors.New("openai images target is not configured")
	}
	if input.Protocol != protocol.ProtocolImagesGenerations && input.Protocol != protocol.ProtocolImagesEdits {
		return result, errors.New("unsupported images protocol")
	}
	if t.Enter != nil {
		done, err := t.Enter()
		if err != nil {
			return result, err
		}
		defer done()
	}
	started := t.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	defer func() {
		if result.Duration == 0 {
			result.Duration = time.Since(started)
		}
		result.Cancelled = errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
		if failure != nil {
			result.FailureClass = "upstream"
		}
	}()
	resp, err := t.Do(t.Request)
	if err != nil {
		return result, t.TransportError(err)
	}
	result.CaptureRequestAttempt(resp)
	// Close the body visible after response policy returns, before admission
	// release; HTTP failures replace the already closed transport body with redacted replay bytes.
	defer func() { _ = resp.Body.Close() }()
	// Record exchange duration before body cleanup and admission release, so
	// connection closing time is excluded from the measured image response processing.
	defer func() { result.Duration = time.Since(started) }()
	if resp.StatusCode >= 400 {
		body := t.ReadErrorBody(resp)
		// Release the failed transport body before health handling consumes replay
		// bytes, and redact that replay so its diagnostics receive no provider credentials.
		_ = resp.Body.Close()
		body = t.RedactErrorBody(body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return result, t.HTTPError(resp, body)
	}
	observed := &imagesObservedSink{OutputSink: sink}
	sink = observed
	defer func() {
		result.HTTPCommitted = observed.committed
		result.RetryCommitted = observed.retryCommitted
		result.Served = result.Served || observed.semantic
		result.ClientDisconnect = observed.failed
	}()
	var usage wire.ForwardUsage
	var output *upstream.OutputContext
	result.RequestID = resp.Header.Get("x-request-id")
	result.UpstreamHeaders = resp.Header
	result.Model = input.ResponseModel
	result.UpstreamModel = t.Model
	result.Stream = input.Stream
	before := 0
	if t.OAuth {
		before = t.Options.AdjustedWrittenSize()
	}
	if input.Stream && (t.OAuth || IsEventStreamResponse(resp.Header)) {
		output = upstream.NewOutputContext(sink)
		if t.OAuth {
			usage, result.ObservedImages, result.ImageOutputSizes, err = ReadImagesOAuthStreaming(resp, output, t.Options, started, t.ResponseFormat, t.StreamPrefix, input.ResponseModel)
		} else {
			usage, result.ObservedImages, result.ImageOutputSizes, err = ReadImagesStreaming(resp, output, t.Options, started)
		}
	} else if t.OAuth {
		usage, result.ObservedImages, result.ImageOutputSizes, err = ReadImagesOAuthNonStreaming(resp, sink, t.Options, t.ResponseFormat, input.ResponseModel)
	} else {
		usage, result.ObservedImages, result.ImageOutputSizes, err = ReadImagesNonStreaming(resp, sink, t.Options)
	}
	result.Usage = upstream.TokenUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		ImageOutputTokens:        usage.ImageOutputTokens,
	}
	result.ImageInputTokens = usage.ImageInputTokens
	result.HasUsage = result.Usage.HasObservedTokens() || usage.ImageInputTokens > 0
	result.Served = result.ObservedImages > 0
	if output != nil {
		result.HTTPCommitted = output.Writer.Written()
	}
	// OAuth response policy handles buffered failures and streaming failure before
	// a completed image; later streaming failures retain the response-reader result for partial output.
	if err != nil && t.OAuth && (!input.Stream || result.ObservedImages <= 0) {
		err = t.ResponseError(resp, before, err)
	}
	return result, err
}

// InitialOutput reads existing output state when the wrapped sink supports it.
// It records the supplied committed flag; sinks without that optional interface
// return an empty head and retain ordinary output initialization.
func (s *imagesObservedSink) InitialOutput() upstream.OutputHead {
	if source, ok := s.OutputSink.(interface {
		// InitialOutput reports the output head already established by this sink.
		// The wrapper reads its committed flag before further image response output
		// begins for this attempt.
		InitialOutput() upstream.OutputHead
	}); ok {
		head := source.InitialOutput()
		s.committed = head.Committed
		return head
	}
	return upstream.OutputHead{}
}

// Begin delegates response commitment and records success or write failure. A
// successful call sets committed, while a failure marks the client-disconnect
// observation for the enclosing attempt result.
func (s *imagesObservedSink) Begin(head upstream.OutputHead) error {
	err := s.OutputSink.Begin(head)
	if err == nil {
		s.committed = true
	} else {
		s.failed = true
	}
	return err
}

// Emit records retry and semantic event flags before delegating delivery. A
// failed write sets failed but retains those attempted-output flags, so callers
// can distinguish completed images from output attempts and downstream
// disconnection.
func (s *imagesObservedSink) Emit(event upstream.OutputEvent) error {
	s.retryCommitted = s.retryCommitted || event.CommitForRetry
	s.semantic = s.semantic || event.Semantic
	err := s.OutputSink.Emit(event)
	if err != nil {
		s.failed = true
	}
	return err
}
