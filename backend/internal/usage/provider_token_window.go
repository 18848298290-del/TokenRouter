package usage

import (
	"context"
	"time"
)

// Provider token windows can be read together when the supplied reader exposes
// a batch method. A batch failure ends the operation; readers with only the
// single-provider method are queried in order, and nil statistics leave that
// provider absent from the returned token map.

// ProviderTokenWindowReader supplies recorded provider counts and costs since a
// start instant.
type ProviderTokenWindowReader interface {
	// GetProviderWindowStats summarizes provider records at or after the start
	// instant. PostgreSQL leaves the upper bound open, including future-dated
	// records, and returns zero-valued statistics when no record matches.
	GetProviderWindowStats(context.Context, int64, time.Time) (*ProviderStats, error)
}

// ReadProviderTokenWindow returns a new map of token counts from the supplied
// provider reader. A nil reader returns nil successfully; a supported batch
// method takes precedence, and its errors end the read without individual
// retries. Missing entries and nil statistics remain absent from the result;
// PostgreSQL supplies zero counts for requested providers with no records.
func ReadProviderTokenWindow(ctx context.Context, reader ProviderTokenWindowReader, ids []int64, start time.Time) (map[int64]int64, error) {
	if reader == nil {
		return nil, nil
	}
	var values map[int64]*ProviderStats
	if batch, ok := reader.(interface {
		// GetProviderWindowStatsBatch reads requested providers using one start
		// instant. PostgreSQL supplies zero-valued statistics for providers with no
		// records; ReadProviderTokenWindow returns an error before using any partial
		// map when this call fails.
		GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*ProviderStats, error)
	}); ok {
		var err error
		values, err = batch.GetProviderWindowStatsBatch(ctx, ids, start)
		if err != nil {
			return nil, err
		}
	} else {
		// Readers lacking the batch capability use their single-provider method.
		// Stop at the first error because these values form one requested window map.
		values = make(map[int64]*ProviderStats, len(ids))
		for _, id := range ids {
			value, err := reader.GetProviderWindowStats(ctx, id, start)
			if err != nil {
				return nil, err
			}
			values[id] = value
		}
	}
	tokens := make(map[int64]int64, len(values))
	for id, value := range values {
		if value != nil {
			tokens[id] = value.Tokens
		}
	}
	return tokens, nil
}
