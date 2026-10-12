package search

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Quota fixtures can increment usage and then report an uncertain result,
// reproducing a reservation reply failure independently of Redis. Their
// decrement path records whether the cleanup context has a deadline before
// deciding whether cancellation permits counter mutation.
//
// Mutable counter observations use one mutex and are read after tested calls
// finish. The uncertain switch is configured before work begins, while release
// counts include attempted decrements even when their context is already
// canceled. Inert executor and proxy operations keep these scenarios focused on
// Manager's confirmed-reservation and lifecycle decisions, rather than supplier
// HTTP exchanges or live marker storage.

// noSearchExecutor supplies successful empty searches and inert client cleanup
// for fixtures.
type noSearchExecutor struct{}

// quotaFixture records supplier-counter operations and can report uncertain
// increments.
type quotaFixture struct {
	// mu protects used, decrements, and releaseDeadline during quota operations.
	// Tests read those observations after their joined calls finish so helper
	// mutations have completed.
	mu               sync.Mutex // quota fixture observation lock
	used, decrements int64      // used: current fixture counter; decrements: release attempt count
	// uncertain makes Increment mutate used before returning an unknown-result
	// error. Configure this switch before concurrent quota calls begin so that
	// scenario stays stable.
	uncertain       bool
	releaseDeadline bool // last decrement had deadline
}

// Search returns a new empty successful response for every fixture call. It
// ignores context, supplier settings, and query input so Manager tests exercise
// their own reservation and publication paths.
func (noSearchExecutor) Search(context.Context, ProviderConfig, SearchRequest) (*SearchResponse, error) {
	return &SearchResponse{}, nil
}

// IsProxyError reports false for every error passed to the inert executor.
func (noSearchExecutor) IsProxyError(error) bool { return false }

// CloseIdle performs no work because this fixture owns no HTTP clients.
func (noSearchExecutor) CloseIdle() {}

// Increment increases fixture usage under mu before returning its outcome. The
// configured uncertain path returns an error after mutation; contexts, supplier
// names, and expiration durations are ignored.
func (q *quotaFixture) Increment(context.Context, string, time.Duration) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used++
	// Return failure after the increment so Manager sees an uncertain reservation
	// even though the fixture's counter has already changed.
	if q.uncertain {
		return 0, errors.New("unknown result")
	}
	return q.used, nil
}

// Decrement records a release attempt and its deadline under mu. An already
// canceled context returns its error before reducing usage, while a live
// context decreases the fixture counter once.
func (q *quotaFixture) Decrement(ctx context.Context, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.decrements++
	_, q.releaseDeadline = ctx.Deadline()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	q.used--
	return nil
}

// Usage reads the fixture's current counter under its observation mutex.
func (q *quotaFixture) Usage(context.Context, string) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.used, nil
}

// Reset reports success without modifying the counter for these reservation
// fixtures.
func (q *quotaFixture) Reset(context.Context, string) error { return nil }

// MarkProxy reports success without retaining proxy markers in this quota
// fixture.
func (q *quotaFixture) MarkProxy(context.Context, int64, time.Duration) error { return nil }

// ProxyAvailable permits every proxy because this fixture stores no failure
// markers.
func (q *quotaFixture) ProxyAvailable(context.Context, int64) bool { return true }
