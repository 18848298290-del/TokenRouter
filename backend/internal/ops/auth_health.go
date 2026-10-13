package ops

import "context"

// Outbox lag, local subscriber health, lookup counters, and invalid-credential
// pressure describe different parts of API-key authentication. The
// administrator response keeps those signals together so operators can inspect
// publication progress alongside the local instance observations.

// GetAuthCacheInvalidationHealth returns outbox progress and available API-key
// service diagnostics. A nil service or missing observer leaves its section
// zero-valued; the supplied context governs the outbox health read, while the
// other readers receive no context argument.
func (s *OpsService) GetAuthCacheInvalidationHealth(ctx context.Context) OpsAuthCacheInvalidationHealth {
	if s == nil {
		return OpsAuthCacheInvalidationHealth{}
	}
	health := OpsAuthCacheInvalidationHealth{}
	if s.authCacheInvalidationWorker != nil {
		health.Outbox = s.authCacheInvalidationWorker.Health(ctx)
	}
	if s.apiKeyService != nil {
		health.Subscriber = s.apiKeyService.AuthCacheInvalidationSubscriberHealth()
		health.Lookup = s.apiKeyService.AuthLookupMetrics()
		health.InvalidAbuse = s.apiKeyService.InvalidAuthAbuseHealth()
	}
	return health
}
