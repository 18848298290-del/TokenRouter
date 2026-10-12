package provider

import "context"

// RetryCooldownStore reads current pool-mode settings and writes temporary scheduling failures.
type RetryCooldownStore interface {
	TemporaryFailureStore
	GetByID(context.Context, int64) (*Record, error)
}

type RetryCooldownInput struct {
	ProviderID             int64
	Status                 int
	Retryable              bool
	RequestScopedTransient bool
}

type RetryCooldownOptions struct {
	Logf        func(string, ...any)
	LookupError func(int64, error)
}

// RetryCooldown applies status-specific cooldowns after retry exhaustion.
type RetryCooldown struct {
	store   RetryCooldownStore
	options RetryCooldownOptions
}

func NewRetryCooldown(store RetryCooldownStore, options RetryCooldownOptions) *RetryCooldown {
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	if options.LookupError == nil {
		options.LookupError = func(int64, error) {}
	}
	return &RetryCooldown{store: store, options: options}
}

func (r *RetryCooldown) Apply(ctx context.Context, input RetryCooldownInput) {
	if r == nil || r.store == nil || !input.Retryable || input.RequestScopedTransient {
		return
	}
	provider, err := r.store.GetByID(ctx, input.ProviderID)
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
