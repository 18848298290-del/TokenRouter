package provider

import (
	"context"
	"slices"
)

// model_sync.go fetches live model catalogues using copied provider records
// and tracked request lifetimes.
// Administrator model previews fetch a live catalogue using a provider snapshot
// whose credentials can be mutated by the adapter. The service copies that
// input before fetching and copies the returned model slice before handing it
// back to its caller.
//
// The shared operationActivity implementation tracks these request-triggered
// queries, combines request and shutdown cancellation, and fixes the first stop
// result. Construction performs no upstream query; requests begin work when
// Fetch is called, and stopped services reject another query.

// ModelSyncService tracks request-triggered model queries through an injected
// fetch function.
type ModelSyncService struct {
	// fetch receives the service-owned provider copy for one registered query.
	// It observes cancellation so shutdown can wait for the call to return.
	fetch func(context.Context, *Record) ([]string, error)
	// activity owns admission, cancellation, and the retained shutdown result.
	// Each successful registration remains active until Fetch's deferred finish.
	activity operationActivity
}

// NewModelSyncService constructs a request-triggered catalogue service around
// fetch. A nil function returns nil to represent an unavailable service, and
// construction performs no upstream request or persistence.
func NewModelSyncService(fetch func(context.Context, *Record) ([]string, error)) *ModelSyncService {
	if fetch == nil {
		return nil
	}
	return &ModelSyncService{fetch: fetch}
}

// Fetch registers a catalogue query and passes a copied provider record to the
// adapter. The returned model slice is copied even when the adapter also
// returns an error.
//
// Request cancellation and service shutdown reach the adapter through the
// registered context. Completion unregisters the operation so StopContext can
// finish waiting, and a stopped service returns ErrRefreshStopped before
// invoking fetch. Callers provide stable input during copying and adapters
// respond to cancellation to permit timely shutdown.
func (s *ModelSyncService) Fetch(ctx context.Context, v *Record) ([]string, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrRefreshStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	models, err := s.fetch(ctx, CloneRecord(v))
	return slices.Clone(models), err
}

// StopContext rejects subsequent model queries, cancels active queries, and
// waits within ctx's budget. The shared activity tracker retains the first stop
// outcome, including a timeout while an adapter remains unfinished.
func (s *ModelSyncService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "provider model list requests")
}
