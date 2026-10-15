package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// request_chat.go sends prepared Chat Completions bodies with selected
// authentication, headers, and transport callbacks.
// Chat requests apply bearer authentication and select JSON or SSE acceptance
// from the prepared stream mode. Allowed inbound headers precede transport
// preparation and final provider header handling, so the selected transport
// identity and overrides can control the outgoing values.

// CCRequestOptions supplies Chat request identity, transport preparation, and
// error callbacks.
type CCRequestOptions struct {
	URL     string      // validated Chat upstream endpoint
	Token   string      `json:"-"` // Chat bearer access token
	Stream  bool        // streaming acceptance mode flag
	Headers http.Header `json:"-"` // permitted inbound header values
	// RequestContext supplies the upstream context and construction-release
	// callback; release runs immediately after building and must leave that context
	// usable for sending.
	RequestContext   func(context.Context) (context.Context, context.CancelFunc)
	ObserveEndpoint  func()                                      // required endpoint diagnostic observer
	AllowHeader      func(string) bool                           // required inbound header predicate
	PrepareTransport func(*http.Request)                         // required transport identity preparer
	FinalizeHeaders  func(http.Header)                           // required final header writer
	Do               func(*http.Request) (*http.Response, error) // required HTTP exchange callback
	TransportError   func(error) error                           // required transport failure translator
}

// SendChatRequest builds and sends a Chat POST with required option callbacks.
// RequestContext release runs after construction and must leave its context
// usable; final headers follow transport preparation, and callers close the
// returned successful response body.
func SendChatRequest(ctx context.Context, body []byte, options CCRequestOptions) (*http.Response, error) {
	upstreamCtx, release := options.RequestContext(ctx)
	request, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, options.URL, bytes.NewReader(body))
	// The context adapter releases construction bookkeeping here; its returned
	// request context must remain live through transport sending and response-body reading.
	release()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	options.ObserveEndpoint()
	request = request.WithContext(upstream.WithHTTPUpstreamProfile(request.Context(), upstream.HTTPUpstreamProfileOpenAI))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+options.Token)
	if options.Stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	for key, values := range options.Headers {
		if options.AllowHeader(strings.ToLower(key)) {
			for _, value := range values {
				request.Header.Add(key, value)
			}
		}
	}
	options.PrepareTransport(request)
	options.FinalizeHeaders(request.Header)
	response, err := options.Do(request)
	if err != nil {
		return nil, options.TransportError(err)
	}
	return response, nil
}

// String returns a fixed Chat request label that omits credentials.
func (CCRequestOptions) String() string { return "openai chat request options" }

// GoString returns the redacted Chat request label for Go-syntax formatting.
func (o CCRequestOptions) GoString() string { return o.String() }
