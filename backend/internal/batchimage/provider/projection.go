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

// Gemini API keys and Vertex service accounts use the same image-batch route
// selection, then execute through platform adapters. The adapter interface keeps
// submission, polling, cancellation, result reading, and cleanup tied to a
// stored job and its provider record.

// These aliases reuse provider platform and credential-type identities when
// batch adapters test whether a record supports their execution path.
const (
	PlatformGemini             = provider.PlatformGemini             // Gemini upstream platform identity
	ProviderTypeAPIKey         = provider.ProviderTypeAPIKey         // API key authentication type
	ProviderTypeServiceAccount = provider.ProviderTypeServiceAccount // service account authentication type
)

type (
	// Provider is the stored provider record passed to batch execution adapters.
	Provider = provider.Record
	// GeminiTokenCache exposes the shared provider token cache used by Vertex
	// service-account authentication.
	GeminiTokenCache = provider.AccessTokenCache
)

// BatchImageProvider executes a stored batch job using its selected provider
// record and platform-specific resources.
type BatchImageProvider interface {
	// Name returns the registry identity used by persisted batch platform bindings.
	Name() string
	// SupportsProvider reports credential compatibility for a provider record.
	// Gemini and Vertex adapters inspect their supported authentication shape;
	// submission separately checks the provider's configured batch protocol before
	// creating resources.
	SupportsProvider(*Provider) bool
	// Submit uploads job input and creates the provider's asynchronous batch.
	// Callers persist the returned references for later polling and cleanup;
	// a failed creation can follow an already uploaded input resource.
	Submit(context.Context, *batchimage.BatchImageJob, *Provider, batchimage.BatchImageInput) (*batchimage.BatchProviderJob, error)
	// Get reads the job's stored provider name and maps one upstream observation.
	// Callers decide polling cadence and lifecycle transitions from the result;
	// existing job reads use their bound provider identity.
	Get(context.Context, *batchimage.BatchImageJob, *Provider) (*batchimage.BatchProviderStatus, error)
	// Cancel requests cancellation of the job's named provider resource.
	// Successful request completion does not establish the job's terminal state;
	// callers continue polling and handle funding release through the job lifecycle.
	Cancel(context.Context, *batchimage.BatchImageJob, *Provider) error
	// OpenResult opens result JSONL using the job's stored provider references.
	// Callers consume and close the returned reader, and use its content type
	// when decoding; reference or provider errors return before a stream is available.
	OpenResult(context.Context, *batchimage.BatchImageJob, *Provider) (io.ReadCloser, string, error)
	// Cleanup deletes stored input or output resources according to the requested
	// target. Callers retain the job references for safe retries and handle partial
	// cleanup when an earlier resource deletion succeeds before a later error.
	Cleanup(context.Context, *batchimage.BatchImageJob, *Provider, batchimage.CleanupTarget) error
}

// batchImageProviderAPIKey returns the trimmed api_key credential or empty for
// a nil record. Callers interpret the empty result as missing credentials
// before asking the Gemini client to authenticate a request.
func batchImageProviderAPIKey(value *Provider) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.GetCredential("api_key"))
}

// resolveBatchProtocol selects the upstream route for the image-batches client
// protocol using the record's routing snapshot. A missing route returns false;
// callers check that flag before treating the protocol ID as enabled.
func resolveBatchProtocol(value *Provider) (capability.ProtocolID, bool) {
	plan := routing.Plan(routing.PlanInput{ClientProtocol: capability.ProtocolImageBatches})
	candidate, ok := plan.ResolveCandidate(value.RoutingSnapshot())
	return candidate.UpstreamProtocol, ok
}
