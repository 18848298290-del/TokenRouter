package provider

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

// Ollama Cloud usage comes from a fixed browser settings fetch with managed
// cookies. The adapter forwards the captured proxy, provider ID, and
// concurrency to the shared sender, while the upstream fetcher receives a
// context that blocks redirects and a stable unavailable error.

// OllamaUsageFetcher returns a cookie-based usage fetch callback backed by the
// supplied sender. A nil sender returns the unavailable error; requests pass
// captured transport parameters and disable redirects through the upstream
// fetch options.
func OllamaUsageFetcher(do func(*http.Request, string, int64, int) (*http.Response, error)) func(context.Context, provider.OllamaUsageFetchInput) (*provider.OllamaUsageObservation, error) {
	return func(ctx context.Context, input provider.OllamaUsageFetchInput) (*provider.OllamaUsageObservation, error) {
		if do == nil {
			return nil, provider.ErrOllamaCloudUsageUnavailable
		}
		return ollama.FetchUsage(ctx, ollama.FetchInput{ObservedAt: input.ObservedAt, Cookie: input.Cookie}, ollama.FetchOptions{
			Do: func(req *http.Request) (*http.Response, error) {
				return do(req, input.ProxyURL, input.ProviderID, input.Concurrency)
			},
			Context:     upstream.WithHTTPUpstreamRedirectsDisabled,
			Unavailable: provider.ErrOllamaCloudUsageUnavailable,
		})
	}
}
