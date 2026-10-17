package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// embeddings.go executes authenticated embedding requests and collects
// response usage and delivery evidence.
// Embedding attempts send the prepared model request with bearer authentication
// and emit the upstream JSON synchronously. Model aliases and the selected
// upstream model remain separate in the result, while usage and served output
// are read from the response body.
//
// The optional admission callback holds activity through body reading and
// output. On an upstream error, the original body is closed before a replay
// reader is passed to HTTPError, so health classification can reread bytes
// after transport ownership ends. RequestContext's release callback runs
// immediately after request construction; its implementation must leave the
// returned context usable for the exchange. A nonempty data array determines
// Served independently of whether output was already committed.

// EmbeddingsTarget contains the selected request identity and synchronous
// embedding execution callbacks.
type EmbeddingsTarget struct {
	ProviderID int64 // selected embedding provider identifier
	// Model is the upstream model, URL is the selected embedding endpoint, and
	// UserAgent is the optional outgoing client identity applied after forwarding.
	Model, URL, UserAgent string
	Token                 string      `json:"-"` // embedding bearer access token
	ForwardHeaders        http.Header `json:"-"` // allowed inbound header values
	// StartedAt measures the full attempt duration; Execute substitutes its current
	// time when the caller supplies a zero timestamp.
	StartedAt time.Time
	// RequestContext returns the upstream context and a construction-release
	// callback; the context must remain usable after that callback is immediately
	// invoked.
	RequestContext func(context.Context) (context.Context, context.CancelFunc)
	// Enter optionally admits activity and returns its release callback; deferred
	// release follows response-body cleanup and synchronous output completion.
	Enter          func() (func(), error)
	ApplyHeaders   func(http.Header)                           // required provider header writer
	Do             func(*http.Request) (*http.Response, error) // required HTTP exchange callback
	TransportError func(error) error                           // required transport error translator
	ReadErrorBody  func(*http.Response) []byte                 // required error body reader
	HTTPError      func(*http.Response, []byte) error          // required upstream status handler
	ReadBody       func(io.Reader) ([]byte, error)             // required successful body reader
	ReadFailure    func(error) error                           // required read error translator
	WriteHeaders   func(http.Header, http.Header)              // required downstream header writer
}

// EmbeddingsExecutor sends prepared embedding requests and reports observed
// usage and response output presence.
type EmbeddingsExecutor struct{}

// TargetID returns the embedding provider identifier or zero for nil targets.
// Diagnostic formatting can therefore describe missing configuration before
// Execute rejects a target that cannot be used.
func (t *EmbeddingsTarget) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String formats the provider identifier with credentials and endpoint omitted.
func (t *EmbeddingsTarget) String() string {
	return fmt.Sprintf("openai embeddings target provider=%d", t.TargetID())
}

// GoString returns the redacted target label for Go-syntax formatting.
func (t *EmbeddingsTarget) GoString() string { return t.String() }

// Execute sends one embedding attempt and reports upstream usage and output
// presence. The target supplies required context, transport, header, read, and
// error callbacks; Enter is optional and StartedAt defaults to the current
// time. The returned context must remain usable after RequestContext's release
// callback runs following construction.
//
// On an HTTP error, this function closes the original body before passing
// captured bytes through a replay reader to HTTPError. Successful bodies are
// read and synchronously emitted unless the output writer is already committed.
// A nonempty data array sets Served, while HasUsage follows the presence of the
// usage object, keeping billing observations separate from downstream write
// state.
func (EmbeddingsExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.CapturePerformance(ctx, failure) }()
	t, ok := input.Target.(*EmbeddingsTarget)
	if !ok || t == nil {
		return result, errors.New("openai embeddings target is not configured")
	}
	if input.Protocol != protocol.ProtocolEmbeddings {
		return result, errors.New("unsupported embeddings protocol")
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
		result.Duration = time.Since(started)
		result.Cancelled = errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
		if failure != nil {
			result.FailureClass = "upstream"
		}
	}()
	upstreamCtx, release := t.RequestContext(ctx)
	request, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, t.URL, bytes.NewReader(input.Body))
	// RequestContext releases construction-scoped bookkeeping here; its returned
	// context must remain valid for Do and response reading after this callback.
	release()
	if err != nil {
		return result, fmt.Errorf("build upstream request: %w", err)
	}
	request = request.WithContext(upstream.WithHTTPUpstreamProfile(request.Context(), upstream.HTTPUpstreamProfileOpenAI))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+t.Token)
	request.Header.Set("Accept", "application/json")
	for key, values := range t.ForwardHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if t.UserAgent != "" {
		request.Header.Set("user-agent", t.UserAgent)
	}
	t.ApplyHeaders(request.Header)
	resp, err := t.Do(request)
	if err != nil {
		return result, t.TransportError(err)
	}
	// Close the body visible on return. The error path closes transport ownership
	// early and replaces it with a replay reader for subsequent error classification.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body := t.ReadErrorBody(resp)
		// Release the original transport before replaying error bytes, so the
		// health callback can inspect the response after connection ownership has ended.
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return result, t.HTTPError(resp, body)
	}
	body, err := t.ReadBody(resp.Body)
	if err != nil {
		return result, t.ReadFailure(err)
	}
	output := upstream.NewOutputContext(sink)
	if !output.Writer.Written() {
		if resp.Header != nil {
			t.WriteHeaders(output.Writer.Header(), resp.Header)
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		output.Writer.Header().Set("Content-Type", contentType)
		output.Writer.WriteHeader(resp.StatusCode)
		// Output failure is discarded here; usage and Served still describe the
		// upstream body rather than successful delivery through the sink.
		_, _ = output.Writer.Write(body)
	}
	usage := wire.ExtractEmbeddingsUsage(body)
	result.RequestID = FirstNonEmptyString(resp.Header.Get("x-request-id"), resp.Header.Get("request-id"))
	result.CaptureRequestAttempt(resp)
	result.UpstreamHeaders = resp.Header
	result.Model = input.ResponseModel
	result.UpstreamModel = t.Model
	result.Usage = upstream.TokenUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
	result.ImageInputTokens = usage.ImageInputTokens
	result.HasUsage = gjson.GetBytes(body, "usage").IsObject()
	result.Served = len(gjson.GetBytes(body, "data").Array()) > 0
	result.HTTPCommitted = output.Writer.Written()
	return result, nil
}
