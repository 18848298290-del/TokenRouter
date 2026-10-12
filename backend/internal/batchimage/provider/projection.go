package provider

import (
	"context"
	"io"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	PlatformGemini             = provider.PlatformGemini
	ProviderTypeAPIKey         = provider.ProviderTypeAPIKey
	ProviderTypeServiceAccount = provider.ProviderTypeServiceAccount
)

type (
	// Provider is the provider record used to execute batch image jobs.
	Provider = provider.Record
	// GeminiTokenCache caches Gemini access tokens.
	GeminiTokenCache = provider.AccessTokenCache
)

// BatchImageProvider submits, polls, cancels, reads results, and cleans up image jobs.
type BatchImageProvider interface {
	Name() string
	SupportsProvider(*Provider) bool
	Submit(context.Context, *batchimage.BatchImageJob, *Provider, batchimage.BatchImageInput) (*batchimage.BatchProviderJob, error)
	Get(context.Context, *batchimage.BatchImageJob, *Provider) (*batchimage.BatchProviderStatus, error)
	Cancel(context.Context, *batchimage.BatchImageJob, *Provider) error
	OpenResult(context.Context, *batchimage.BatchImageJob, *Provider) (io.ReadCloser, string, error)
	Cleanup(context.Context, *batchimage.BatchImageJob, *Provider, batchimage.CleanupTarget) error
}

// batchImageProviderAPIKey returns the trimmed provider API key, or empty for nil.
func batchImageProviderAPIKey(value *Provider) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.GetCredential("api_key"))
}

// resolveBatchProtocol returns the matched upstream batch protocol and whether a route exists.
func resolveBatchProtocol(value *Provider) (capability.ProtocolID, bool) {
	plan := routing.Plan(routing.PlanInput{ClientProtocol: capability.ProtocolImageBatches})
	candidate, ok := plan.ResolveCandidate(value.RoutingSnapshot())
	return candidate.UpstreamProtocol, ok
}
