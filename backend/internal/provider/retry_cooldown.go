package provider

import "context"

// Retry exhaustion can describe either a provider failure or a request-specific
// transient response. The request-scoped flag prevents a persistent cooldown
// before any lookup. Eligible attempts reload the provider so its current pool
// setting can suppress the local pause even when the request used an older
// snapshot.
//
// The reloaded record controls pool handling, while the attempt's status
// chooses the fixed failure command: 400 selects Google configuration pause and
// 502 selects empty-response pause. Other statuses perform no pause write. A
// lookup error is reported and ends handling; a nil successful lookup proceeds
// as non-pool. Each selected command logs its separate store result, leaving
// retry exhaustion and persistent pause publication as distinct outcomes.

// RetryCooldownStore supplies current provider reads and retry-exhaustion pause
// writes for cooldown selection.
type RetryCooldownStore interface {
	// TemporaryFailureStore supplies the selected short pause command after
	// provider lookup. The core uses separate calls; the store implementation
	// controls transaction participation across those calls.
	TemporaryFailureStore // persistent pause methods
	// GetByID reads current pool configuration for the selected provider. A read
	// error stops cooldown selection; a nil successful result is treated as
	// non-pool by Apply and can still reach the pause call.
	GetByID(context.Context, int64) (*Record, error)
}

// RetryCooldownInput holds the attempt status and eligibility flags used after
// retry exhaustion.
type RetryCooldownInput struct {
	// ProviderID and Status connect the current provider lookup to the attempt's
	// selected failure command. A matching status can attempt persistence after a
	// successful lookup, including a nil record.
	ProviderID int64 // affected provider ID
	Status     int   // attempt HTTP status
	// Retryable admits cooldown handling when true; false returns before lookup so
	// ordinary nonretryable failures keep their selected response handling.
	Retryable bool
	// RequestScopedTransient excludes this attempt from provider persistence even
	// when Retryable is true, keeping request-specific failures from installing a
	// shared scheduling pause.
	RequestScopedTransient bool
}

// RetryCooldownOptions supplies synchronous diagnostics for lookup and
// persistent pause outcomes.
type RetryCooldownOptions struct {
	// Logf receives the selected pause command's success or failure;
	// NewRetryCooldown supplies a no-op when this callback is absent.
	Logf func(string, ...any)
	// LookupError receives the provider ID and failed read before Apply returns;
	// NewRetryCooldown supplies a no-op when absent.
	LookupError func(int64, error)
}

// RetryCooldown retains current-provider reads and diagnostics for
// retry-exhaustion pause selection.
type RetryCooldown struct {
	// store remains borrowed for synchronous Apply calls. The application owns its
	// resources and keeps it available while provider lookup and selected pause
	// publication are running.
	store   RetryCooldownStore
	options RetryCooldownOptions // normalized diagnostic callbacks
}

// NewRetryCooldown retains the store and replaces absent diagnostic callbacks
// with no-ops. Construction performs no reads; a missing store keeps Apply
// idle, and the application owns store lifetime during synchronous calls.
func NewRetryCooldown(store RetryCooldownStore, options RetryCooldownOptions) *RetryCooldown {
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	if options.LookupError == nil {
		options.LookupError = func(int64, error) {}
	}
	return &RetryCooldown{store: store, options: options}
}

// Apply selects a short persistent pause after an eligible retry-exhaustion
// response. Nil service/store, a false Retryable flag or a request-scoped
// transient returns before provider lookup. Supply a usable context and keep
// options stable during the synchronous call.
//
// The current record's pool setting takes precedence over the attempt status,
// so pool records receive no local pause. Lookup failure is observed and stops
// selection, while a nil successful lookup proceeds as non-pool. Status 400
// selects the configuration command and 502 selects the empty-response command;
// other statuses leave persistence skipped. Those commands report their own
// store outcome through Logf.
func (r *RetryCooldown) Apply(ctx context.Context, input RetryCooldownInput) {
	if r == nil || r.store == nil || !input.Retryable || input.RequestScopedTransient {
		return
	}
	provider, err := r.store.GetByID(ctx, input.ProviderID)
	// A failed lookup leaves current pool policy unverified. Report it and skip
	// pause selection instead of using the request's possibly stale provider
	// configuration.
	if err != nil {
		r.options.LookupError(input.ProviderID, err)
		return
	}
	if provider != nil && provider.IsPoolMode() {
		return
	}
	switch input.Status {
	case 400:
		TempUnscheduleGoogleConfigError(ctx, r.store, input.ProviderID, "[handler]", r.options.Logf)
	case 502:
		TempUnscheduleEmptyResponse(ctx, r.store, input.ProviderID, "[handler]", r.options.Logf)
	}
}
