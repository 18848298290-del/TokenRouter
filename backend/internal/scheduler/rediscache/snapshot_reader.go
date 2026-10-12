package rediscache

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// SnapshotReader retrieves SnapshotService values and unwraps them as provider records.
type SnapshotReader struct{ source *scheduler.SnapshotService }

func NewSnapshotReader(source *scheduler.SnapshotService) *SnapshotReader {
	return &SnapshotReader{source: source}
}

func (s *SnapshotReader) GetProvider(ctx context.Context, id int64) (*provider.Record, error) {
	value, err := s.source.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	return codec.RecordValue(value)
}

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
