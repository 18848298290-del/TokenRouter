package apikey

import (
	"time"
)

// now reads the configured clock on each call. Configure the clock before starting the service.
func (s *APIKeyService) now() time.Time {
	if s != nil && s.cfg != nil && s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func (w *AuthCacheInvalidationWorker) now() time.Time {
	if w.local != nil {
		return w.local.now()
	}
	return time.Now()
}
