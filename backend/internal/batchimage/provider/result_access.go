package provider

import (
	"context"
	"io"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// ResultProviders reads the execution provider bound to a job.
type ResultProviders interface {
	GetByID(context.Context, int64) (*provider.Record, error)
}

// ResultAccess resolves providers and applies download, cleanup, or processing eligibility checks.
type ResultAccess struct {
	Registry  *batchimage.Registry[BatchImageProvider]
	Providers ResultProviders
}

// boundProvider binds operations to a job provider and keeps credentials out of public results.
type boundProvider struct {
	platform BatchImageProvider
	provider *provider.Record
}

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
	if err != nil {
		return nil, batchimage.ErrBatchImageDownloadFailed
	}
	if !selected.SupportsProvider(provider.CloneRecord(value)) {
		return nil, batchimage.ErrBatchImageProviderUnsupportedProvider
	}
	return BindProvider(selected, value), nil
}

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
	return BindProvider(selected, value), nil
}

func (b boundProvider) OpenResult(ctx context.Context, job *batchimage.BatchImageJob) (io.ReadCloser, string, error) {
	return b.platform.OpenResult(ctx, job, provider.CloneRecord(b.provider))
}

func (b boundProvider) Cleanup(ctx context.Context, job *batchimage.BatchImageJob, target batchimage.CleanupTarget) error {
	return b.platform.Cleanup(ctx, job, provider.CloneRecord(b.provider), target)
}

func (b boundProvider) Submit(ctx context.Context, job *batchimage.BatchImageJob, input batchimage.BatchImageInput) (*batchimage.BatchProviderJob, error) {
	return b.platform.Submit(ctx, job, provider.CloneRecord(b.provider), input)
}

func (b boundProvider) Get(ctx context.Context, job *batchimage.BatchImageJob) (*batchimage.BatchProviderStatus, error) {
	return b.platform.Get(ctx, job, provider.CloneRecord(b.provider))
}

func (b boundProvider) Cancel(ctx context.Context, job *batchimage.BatchImageJob) error {
	return b.platform.Cancel(ctx, job, provider.CloneRecord(b.provider))
}

// Process checks execution eligibility and propagates provider lookup errors.
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

// BindProvider binds this provider snapshot; each operation receives an independent copy.
func BindProvider(selected BatchImageProvider, value *provider.Record) batchimage.ExecutionProvider {
	return boundProvider{selected, value}
}

func (b boundProvider) Name() string { return b.platform.Name() }
