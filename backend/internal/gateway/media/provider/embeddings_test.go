package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// providerOutput captures emitted embedding bytes and response status for
// adapter assertions.
type providerOutput struct {
	status int // last begun HTTP status
	// data accumulates copies of emitted payload bytes through append. The
	// fixture retains the slice until serial assertions inspect the complete
	// embedding response.
	data []byte // owned accumulated response bytes
}

// trackedMediaBody observes native response closure before delegating to
// its retained reader.
type trackedMediaBody struct {
	// ReadCloser and closed share the response reader and its external
	// observation flag. Close marks that flag before closing the reader so the
	// activity callback can assert cleanup ordering.
	io.ReadCloser              // wrapped response body reader
	closed        *atomic.Bool // shared response-closed observation flag
}

// Begin records the HTTP status supplied by the native embedding response.
func (s *providerOutput) Begin(head upstream.OutputHead) error { s.status = head.Status; return nil }

// Emit appends a copy of event bytes to the fixture response. Calls and
// assertions run serially, and retained event storage can be reused after
// this method returns.
func (s *providerOutput) Emit(event upstream.OutputEvent) error {
	s.data = append(s.data, event.Data...)
	return nil
}

// Close marks response closure before invoking the wrapped body close. Each
// call delegates again, so this fixture relies on native execution for
// close frequency rather than enforcing idempotence itself.
func (b trackedMediaBody) Close() error {
	// Native cleanup marks its close attempt before calling the wrapped
	// reader, so the observation remains true even if that close returns an
	// error.
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

// TestEmbeddingsAdapterOwnsNativeTargetAndResponse sends an embedding
// request through a local server with captured gateway callbacks. It checks
// constructed headers, metadata and usage mapping, credential-safe
// formatting, and body closure before activity completion.
func TestEmbeddingsAdapterOwnsNativeTargetAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer sensitive-fixture", r.Header.Get("Authorization"))
		require.Equal(t, "custom", r.Header.Get("X-Override"))
		require.Equal(t, "forwarded", r.Header.Get("X-Forwarded-Test"))
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"upstream","input":["a","b"]}`, string(data))
		w.Header().Set("X-Request-Id", "embedding-id")
		_, _ = io.WriteString(w, `{"data":[{"embedding":[1,2]}],"usage":{"prompt_tokens":4}}`)
	}))
	// Keep the local server alive through adapter execution and assertions,
	// then close its connections when the test returns.
	defer server.Close()
	var closed atomic.Bool
	var ended atomic.Bool
	options := EmbeddingsOptions{ProviderID: 3, Model: "upstream", URL: server.URL, Token: "sensitive-fixture", ForwardHeaders: http.Header{"X-Forwarded-Test": []string{"forwarded"}}, RequestContext: func(ctx context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} }, ApplyHeaders: func(headers http.Header) { headers.Set("X-Override", "custom") }, Enter: func() (func(), error) {
		return func() {
			// Activity completion observes the body-closed flag before recording its
			// own end, protecting native resource cleanup ordering on this success
			// path.
			require.True(t, closed.Load())
			ended.Store(true)
		}, nil
	}, Do: func(r *http.Request) (*http.Response, error) {
		resp, err := server.Client().Do(r)
		if resp != nil {
			resp.Body = trackedMediaBody{resp.Body, &closed}
		}
		return resp, err
	}, ReadBody: io.ReadAll, WriteHeaders: func(dst, src http.Header) {
		for k, v := range src {
			dst[k] = append([]string(nil), v...)
		}
	}}
	require.NotContains(t, fmt.Sprintf("%#v", options), "sensitive-fixture")
	output := &providerOutput{}
	result, err := (Embeddings{Options: options}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolEmbeddings, Body: []byte(`{"model":"upstream","input":["a","b"]}`), ResponseModel: "client"}, output)
	require.NoError(t, err)
	require.Equal(t, 200, output.status)
	require.True(t, strings.Contains(string(output.data), "embedding"))
	require.Equal(t, "embedding-id", result.RequestID)
	require.Equal(t, "client", result.Model)
	require.Equal(t, "upstream", result.UpstreamModel)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.True(t, closed.Load())
	require.True(t, ended.Load())
}
