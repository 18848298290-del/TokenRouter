package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestDetachedUpstreamReceivesInternalAbort checks that both detachment entry points
// ignore client cancellation and still receive server aborts.
func TestDetachedUpstreamReceivesInternalAbort(t *testing.T) {
	for _, stream := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			client, cancelClient := context.WithCancel(context.Background())
			request, abort := requestcontext.WithAbort(client)
			defer abort()
			var upstream context.Context
			var release context.CancelFunc
			if stream {
				upstream, release = DetachStreamUpstreamContext(request, true)
			} else {
				upstream, release = DetachUpstreamContext(request)
			}
			defer release()
			cancelClient()
			synctest.Wait()
			require.NoError(t, upstream.Err())
			abort()
			synctest.Wait()
			require.ErrorIs(t, upstream.Err(), context.Canceled)
		})
	}
}

// TestOpenAIRequestContextLifetime checks that sending leaves the context alive
// for body reads and that a later server abort cancels it.
func TestOpenAIRequestContextLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("response"))
	}))
	defer server.Close()
	for _, kind := range []string{"chat", "exchange", "guarded_exchange"} {
		t.Run(kind, func(t *testing.T) {
			request, abort := requestcontext.WithAbort(context.Background())
			defer abort()
			var sent context.Context
			send := func(r *http.Request) (*http.Response, error) {
				sent = r.Context()
				return http.DefaultClient.Do(r)
			}
			var response *http.Response
			var err error
			if kind == "chat" {
				response, err = openai.SendChatRequest(request, []byte(`{}`), openai.CCRequestOptions{
					URL: server.URL, RequestContext: DetachUpstreamContext,
					ObserveEndpoint: func() {}, AllowHeader: func(string) bool { return false },
					PrepareTransport: func(*http.Request) {}, FinalizeHeaders: func(http.Header) {},
					Do: send, TransportError: func(err error) error { return err },
				})
			} else {
				timeout := time.Duration(0)
				if kind == "guarded_exchange" {
					timeout = time.Minute
				}
				response, err = openai.ExchangeHTTP(request, nil, openai.HTTPExchangeOptions{
					StartedAt: time.Now(), FirstOutputTimeout: timeout, RequestContext: DetachUpstreamContext,
					Build: func(ctx context.Context, _ []byte) (*http.Request, error) {
						return http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
					},
					ApplyHeaders: func(http.Header) {}, Do: send, Latency: func(time.Duration) {},
					HeaderTimeout: func() error { return context.DeadlineExceeded }, TransportError: func(err error) error { return err },
				})
			}
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.NoError(t, sent.Err())
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "response", string(body))
			abort()
			require.Eventually(t, func() bool { return sent.Err() != nil }, time.Second, time.Millisecond)
		})
	}
}
