package apikey

import (
	"time"
)

// Key expiry and outbox scheduling use the service clock when a caller supplies
// one. Authentication reads that callback at each decision, and the worker
// delegates to the same service so deterministic callers can keep both kinds of
// timestamps consistent.

// now returns the configured service time, or the current wall-clock time when
// absent. Nil services and nil Options use time.Now; install a concurrency-safe
// callback before other callers start using the service.
func (s *APIKeyService) now() time.Time {
	if s != nil && s.cfg != nil && s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// now returns time from the bound authentication service when one is available.
// A worker without a local service uses time.Now; callers invoke this method on
// a nonnil worker instance.
func (w *AuthCacheInvalidationWorker) now() time.Time {
	if w.local != nil {
		return w.local.now()
	}
	return time.Now()
}
