package provider

import (
	"context"
	"io"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// result_access.go binds stored batch-job providers for downloads, processing,
// and resource cleanup.
// Existing batch operations resolve the platform and provider ID stored on the
// job. Download hides provider lookup errors, processing retains them, and cleanup
// can proceed without the adapter's eligibility check, allowing resource removal
// under its own credential and path rules.

// ResultProviders reads the provider record identified by a persisted batch
// job binding.
type ResultProviders interface {
	// GetByID returns a provider record for the stored provider ID and any lookup
	// error. ResultAccess binds a successful record for later operations, which
	// each receive a CloneRecord view.
	GetByID(context.Context, int64) (*provider.Record, error)
}

// ResultAccess resolves persisted provider bindings for batch download,
// processing, and cleanup use cases.
type ResultAccess struct {
	// Registry and Providers are retained for stored job bindings. Configure both
	// before Process or Cleanup and keep them available through bound operations;
	// Download converts either missing dependency into its fixed download error.
	Registry  *batchimage.Registry[BatchImageProvider] // persisted platform adapter lookup
	Providers ResultProviders                          // persisted provider record reader
}

// boundProvider retains a platform adapter and provider record for job-bound
// operations using per-call record cloning.
type boundProvider struct {
	// platform and provider are retained together for the stored job binding.
	// Callers pass that matching job to operations and keep provider stable while
	// CloneRecord prepares each operation's view.
	platform BatchImageProvider // selected batch platform adapter
	// provider remains the supplied record reference after BindProvider.
	// Per-operation cloning copies CloneRecord's supported mutable shapes; arbitrary
	// other values can remain shared according to that helper's rules.
	provider *provider.Record // retained execution provider record
}

// Download resolves the job's stored platform and provider ID, then checks
// credential compatibility before binding operations. Missing dependencies or
// lookup failures become a download error; absent platform and identity
// bindings retain their specific application errors.
func (a ResultAccess) Download(ctx context.Context, job *batchimage.BatchImageJob) (batchimage.BoundProvider, error) {
	if a.Registry == nil || a.Providers == nil || job == nil {
		return nil, batchimage.ErrBatchImageDownloadFailed
	}
	selected, ok := a.Registry.Get(job.Platform)
	if !ok || selected == nil {
		return nil, batchimage.ErrBatchImageUnsupportedProvider
	}
	if job.ProviderID == nil || *job.ProviderID <= 0 {
		return nil, batchimage.ErrBatchImageMissingProviderID
	}
	value, err := a.Providers.GetByID(ctx, *job.ProviderID)
	// Downloads expose a fixed failure for provider lookup errors because storage
	// diagnostics can reveal internal provider details. Platform and missing-ID
	// errors retain their separate client decisions.
	if err != nil {
		return nil, batchimage.ErrBatchImageDownloadFailed
	}
	if !selected.SupportsProvider(provider.CloneRecord(value)) {
		return nil, batchimage.ErrBatchImageProviderUnsupportedProvider
	}
	return BindProvider(selected, value), nil
}

// Cleanup binds the job's stored platform and provider ID for resource removal.
// Callers provide a job and provider reader; lookup errors propagate, and
// adapter-specific cleanup performs its own credential and path checks.
func (a ResultAccess) Cleanup(ctx context.Context, job *batchimage.BatchImageJob) (batchimage.BoundProvider, error) {
	selected, ok := a.Registry.Get(job.Platform)
	if !ok || selected == nil {
		return nil, batchimage.ErrBatchImageUnsupportedProvider
	}
	if job.ProviderID == nil || *job.ProviderID <= 0 {
		return nil, batchimage.ErrBatchImageMissingProviderID
	}
	value, err := a.Providers.GetByID(ctx, *job.ProviderID)
	if err != nil {
		return nil, err
	}
	// Cleanup binds the persisted provider even when admission eligibility changed.
	// The cleanup adapter still validates credentials and managed resource paths
	// before attempting the job's stored deletions.
	return BindProvider(selected, value), nil
}

// OpenResult delegates result reading with a fresh CloneRecord view of the
// retained provider. The caller uses the matching bound job and closes the
// returned stream after consuming its provider results.
func (b boundProvider) OpenResult(ctx context.Context, job *batchimage.BatchImageJob) (io.ReadCloser, string, error) {
	return b.platform.OpenResult(ctx, job, provider.CloneRecord(b.provider))
}

// Cleanup delegates the selected resource target with a fresh CloneRecord
// view. The caller supplies the matching bound job and handles adapter errors,
// including partial deletions that may require retry.
func (b boundProvider) Cleanup(ctx context.Context, job *batchimage.BatchImageJob, target batchimage.CleanupTarget) error {
	return b.platform.Cleanup(ctx, job, provider.CloneRecord(b.provider), target)
}

// Submit delegates batch creation with a fresh CloneRecord view of the retained
// provider. The caller supplies the matching job and input, then persists
// returned provider references for polling and cleanup.
func (b boundProvider) Submit(ctx context.Context, job *batchimage.BatchImageJob, input batchimage.BatchImageInput) (*batchimage.BatchProviderJob, error) {
	return b.platform.Submit(ctx, job, provider.CloneRecord(b.provider), input)
}

// Get delegates one upstream job observation with a fresh CloneRecord view.
// The caller supplies the matching bound job and uses returned state to decide
// the next lifecycle or polling action.
func (b boundProvider) Get(ctx context.Context, job *batchimage.BatchImageJob) (*batchimage.BatchProviderStatus, error) {
	return b.platform.Get(ctx, job, provider.CloneRecord(b.provider))
}

// Cancel delegates a cancellation request with a fresh CloneRecord view of
// the retained provider. The caller supplies the matching bound job and later
// observes its provider state before finalizing cancellation.
func (b boundProvider) Cancel(ctx context.Context, job *batchimage.BatchImageJob) error {
	return b.platform.Cancel(ctx, job, provider.CloneRecord(b.provider))
}

// Process resolves the job's stored platform and provider ID, then checks
// credential compatibility for further execution. Callers provide a job and
// provider reader; lookup errors propagate and unsupported records return the
// adapter eligibility error.
func (a ResultAccess) Process(ctx context.Context, job *batchimage.BatchImageJob) (batchimage.BoundProvider, error) {
	selected, ok := a.Registry.Get(job.Platform)
	if !ok || selected == nil {
		return nil, batchimage.ErrBatchImageUnsupportedProvider
	}
	if job.ProviderID == nil || *job.ProviderID <= 0 {
		return nil, batchimage.ErrBatchImageMissingProviderID
	}
	value, err := a.Providers.GetByID(ctx, *job.ProviderID)
	if err != nil {
		return nil, err
	}
	if !selected.SupportsProvider(provider.CloneRecord(value)) {
		return nil, batchimage.ErrBatchImageProviderUnsupportedProvider
	}
	return BindProvider(selected, value), nil
}

// BindProvider retains the supplied platform adapter and provider record for
// later operations. Callers keep that record stable and use its matching job;
// each operation calls CloneRecord, whose supported-container copying rules
// govern any mutable credential values.
func BindProvider(selected BatchImageProvider, value *provider.Record) batchimage.ExecutionProvider {
	return boundProvider{selected, value}
}

// Name returns the retained platform adapter's batch registry identity.
func (b boundProvider) Name() string { return b.platform.Name() }
