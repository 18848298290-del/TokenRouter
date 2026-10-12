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

// ImagesTarget holds a prepared image attempt. Do not serialize credentials in Request.
type ImagesTarget struct {
	ProviderID                          int64
	OAuth                               bool
	Model, ResponseFormat, StreamPrefix string
	StartedAt                           time.Time
	Request                             *http.Request `json:"-"`
	Options                             ImageResponseOptions
	Enter                               func() (func(), error)
	Do                                  func(*http.Request) (*http.Response, error)
	TransportError                      func(error) error
	ReadErrorBody                       func(*http.Response) []byte
	RedactErrorBody                     func([]byte) []byte
	HTTPError                           func(*http.Response, []byte) error
	ResponseError                       func(*http.Response, int, error) error
}

// ImagesExecutor sends one image request to the selected provider.
type ImagesExecutor struct{}

// imagesObservedSink records synchronous output commitment, content and write failures.
type imagesObservedSink struct {
	upstream.OutputSink
	committed, retryCommitted, semantic, failed bool
}

func (t *ImagesTarget) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

func (t *ImagesTarget) String() string {
	return fmt.Sprintf("openai images target provider=%d", t.TargetID())
}
func (t *ImagesTarget) GoString() string { return t.String() }

func (ImagesExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
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
	// Close the final response body after result handling, before releasing activity registration.
	defer func() { _ = resp.Body.Close() }()
	// Measure duration before closing the response body.
	defer func() { result.Duration = time.Since(started) }()
	if resp.StatusCode >= 400 {
		body := t.ReadErrorBody(resp)
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
			usage, result.ObservedImages, result.ImageOutputSizes, result.FirstTokenMs, err = ReadImagesOAuthStreaming(resp, output, t.Options, started, t.ResponseFormat, t.StreamPrefix, input.ResponseModel)
		} else {
			usage, result.ObservedImages, result.ImageOutputSizes, result.FirstTokenMs, err = ReadImagesStreaming(resp, output, t.Options, started)
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
	// OAuth failures before any completed image, or in buffered mode, use ResponseError.
	if err != nil && t.OAuth && (!input.Stream || result.ObservedImages <= 0) {
		err = t.ResponseError(resp, before, err)
	}
	return result, err
}

func (s *imagesObservedSink) InitialOutput() upstream.OutputHead {
	if source, ok := s.OutputSink.(interface{ InitialOutput() upstream.OutputHead }); ok {
		head := source.InitialOutput()
		s.committed = head.Committed
		return head
	}
	return upstream.OutputHead{}
}

func (s *imagesObservedSink) Begin(head upstream.OutputHead) error {
	err := s.OutputSink.Begin(head)
	if err == nil {
		s.committed = true
	} else {
		s.failed = true
	}
	return err
}

func (s *imagesObservedSink) Emit(event upstream.OutputEvent) error {
	s.retryCommitted = s.retryCommitted || event.CommitForRetry
	s.semantic = s.semantic || event.Semantic
	err := s.OutputSink.Emit(event)
	if err != nil {
		s.failed = true
	}
	return err
}
