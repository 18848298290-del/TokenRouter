package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// CCRequestOptions supplies transport and platform-specific header callbacks for Chat requests.
type CCRequestOptions struct {
	URL              string
	Token            string `json:"-"`
	Stream           bool
	Headers          http.Header `json:"-"`
	RequestContext   func(context.Context) (context.Context, context.CancelFunc)
	ObserveEndpoint  func()
	AllowHeader      func(string) bool
	PrepareTransport func(*http.Request)
	FinalizeHeaders  func(http.Header)
	Do               func(*http.Request) (*http.Response, error)
	TransportError   func(error) error
}

// SendChatRequest builds and sends a Chat request after transport preparation and final header overrides.
// The caller owns the returned response body.
func SendChatRequest(ctx context.Context, body []byte, options CCRequestOptions) (*http.Response, error) {
	upstreamCtx, release := options.RequestContext(ctx)
	request, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, options.URL, bytes.NewReader(body))
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

// String returns a label that omits credentials and forwarded headers.
func (CCRequestOptions) String() string     { return "openai chat request options" }
func (o CCRequestOptions) GoString() string { return o.String() }
