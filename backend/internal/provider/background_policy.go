package provider

// Lock contention and an already fresh provider both finish background work
// without exchanging credentials. The selected skip policy converts each case
// into either ErrRefreshSkipped or success, which determines how page workers
// count the result.

// Background skip actions determine worker statistics when coordinated refresh
// returns contention or a provider that needs no exchange.
const (
	// BackgroundSkipAsSkipped counts an unperformed refresh as skipped by
	// default.
	BackgroundSkipAsSkipped BackgroundSkipAction = iota
	// BackgroundSkipAsSuccess counts an unperformed refresh as successful.
	BackgroundSkipAsSuccess
)

// BackgroundSkipAction selects the statistics outcome for an unperformed
// credential exchange.
type BackgroundSkipAction int

// BackgroundRefreshPolicy independently classifies distributed contention and
// current credentials that require no exchange for background workers.
type BackgroundRefreshPolicy struct {
	OnLockHeld       BackgroundSkipAction // distributed lock contention
	OnAlreadyRefresh BackgroundSkipAction // reread needs no exchange
}

// DefaultBackgroundRefreshPolicy counts contention and fresh credentials as
// skipped background work.
func DefaultBackgroundRefreshPolicy() BackgroundRefreshPolicy {
	return BackgroundRefreshPolicy{
		OnLockHeld:       BackgroundSkipAsSkipped,
		OnAlreadyRefresh: BackgroundSkipAsSkipped,
	}
}

// HandleLockHeld classifies contention using OnLockHeld and returns nil for the
// success action. Every other value, including the zero value, returns
// ErrRefreshSkipped for worker accounting.
func (p BackgroundRefreshPolicy) HandleLockHeld() error {
	if p.OnLockHeld == BackgroundSkipAsSuccess {
		return nil
	}
	return ErrRefreshSkipped
}

// HandleAlreadyRefreshed classifies a reread that requires no exchange using
// OnAlreadyRefresh. The success action returns nil; zero and unrecognized
// actions return ErrRefreshSkipped for worker accounting.
func (p BackgroundRefreshPolicy) HandleAlreadyRefreshed() error {
	if p.OnAlreadyRefresh == BackgroundSkipAsSuccess {
		return nil
	}
	return ErrRefreshSkipped
}
