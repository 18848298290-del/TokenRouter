package httpapi

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Image URL backfill is an optional API Key delivery conversion, using the
// selected provider's proxy and shared URL validator. The native downloader
// adds a per-request public-host restriction, preserves signed URL spelling
// and reports failed item indexes while leaving available response metadata
// for the client.

// imageBackfillOptions binds provider eligibility, URL validation and
// unauthenticated download transport for native backfill. A nil executor
// supplies no HTTP transport; a usable executor requires Requests. Provider
// failures log only provider ID and item index, while successful downloads
// reuse its configured proxy.
func (s *OpenAIImagesExecutor) imageBackfillOptions(provider *gatewayprovider.ExecutionProvider) openai.ImageBackfillOptions {
	options := openai.ImageBackfillOptions{Enabled: gatewayprovider.ImagesURLToB64JSONEnabled(provider)}
	if s != nil {
		options.ValidateURL = s.Requests.ValidateURL
		if s.Requests.Transport != nil && provider != nil {
			proxyURL := ""
			if provider.Record.ProxyID != nil && provider.Record.Proxy != nil {
				proxyURL = provider.Record.Proxy.URL()
			}
			options.Do = func(request *http.Request) (*http.Response, error) {
				return s.Requests.Transport.Do(request, proxyURL, provider.Record.ID, provider.Record.Concurrency)
			}
		}
	}
	if provider != nil {
		options.Failure = func(index int) {
			logging.LegacyPrintf("service.openai_gateway", "[OpenAI] Images b64_json backfill skipped provider_id=%d index=%d: image download or conversion failed", provider.Record.ID, index)
		}
	}
	return options
}

// backfillOpenAIImagesB64JSON delegates optional filling of missing Base64
// fields after response observations have been collected. Parsed input
// supplies stream and response-format restrictions; nil leaves them unset.
// Disabled or incompatible responses return the original body. Failed
// eligible downloads preserve existing item fields, while successful
// neighboring items can still be filled.
func (s *OpenAIImagesExecutor) backfillOpenAIImagesB64JSON(ctx context.Context, provider *gatewayprovider.ExecutionProvider, parsed *media.ImageRequest, body []byte) []byte {
	options := s.imageBackfillOptions(provider)
	if parsed != nil {
		options.Stream = parsed.Stream
		options.ResponseFormat = parsed.ResponseFormat
	}
	return options.Backfill(ctx, body)
}
