package rediscache

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// Execution adapters need provider Records after SnapshotService has applied
// cache lookup, database fallback and bucket rules. The reader unwraps the
// codec's snapshot values and forwards the service's result flag, copying
// outer list values while retaining each record's nested data.

// SnapshotReader retrieves controlled SnapshotService values as provider
// Records for execution adapters.
type SnapshotReader struct {
	// source is the caller's shared snapshot service; the reader relies on its
	// lookup, fallback and runtime lifetime.
	source *scheduler.SnapshotService // shared scheduler snapshot service
}

// NewSnapshotReader wraps the supplied snapshot service for provider adapter
// reads. The caller supplies a usable service and retains its runtime
// ownership while this reader is in use.
func NewSnapshotReader(source *scheduler.SnapshotService) *SnapshotReader {
	return &SnapshotReader{source: source}
}

// GetProvider retrieves and unwraps a full provider record through
// SnapshotService. A missing value returns nil; unsupported snapshot
// implementations return codec errors, and returned nested data remains owned
// by that record.
func (s *SnapshotReader) GetProvider(ctx context.Context, id int64) (*provider.Record, error) {
	value, err := s.source.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	return codec.RecordValue(value)
}

// ListProviders unwraps the service's bucket candidates and forwards its
// result flag. Nil lists remain nil and nil records are skipped; copied
// Record values retain shared nested maps and pointers.
func (s *SnapshotReader) ListProviders(ctx context.Context, group *int64, platform string, forced bool) ([]provider.Record, bool, error) {
	values, mixed, err := s.source.ListSchedulableProviders(ctx, group, platform, forced)
	if err != nil {
		return nil, mixed, err
	}
	if values == nil {
		return nil, mixed, nil
	}
	out := make([]provider.Record, 0, len(values))
	for _, value := range values {
		record, err := codec.RecordValue(value)
		if err != nil {
			return nil, mixed, err
		}
		if record != nil {
			out = append(out, *record)
		}
	}
	return out, mixed, nil
}
