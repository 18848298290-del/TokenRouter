package apikey

import (
	"context"
)

// Credential, owner and group changes invalidate the local authentication
// entries and broadcast their hashed cache identities to other instances.
// Service invalidation joins the operation gate during shutdown, and failed
// list reads or cache calls leave recovery to later cache expiry or durable
// outbox delivery.

// InvalidateAuthCacheByKey deletes and broadcasts one credential's
// authentication cache identity while service work is admitted. Nil or stopping
// services and empty credentials do nothing; cache failures are suppressed for
// the caller.
func (s *APIKeyService) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	if s == nil || !s.operations.enter() {
		return
	}
	defer s.operations.leave()

	if key == "" {
		return
	}
	cacheKey := s.KeyAuthCacheKey(key)
	s.KeyDeleteAuthCache(ctx, cacheKey)
}

// InvalidateAuthCacheByUserID lists nondeleted credentials for userID and
// invalidates their cached identities. Nil or stopping services and nonpositive
// IDs do nothing; a repository failure skips this invalidation attempt and is
// not returned.
func (s *APIKeyService) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	if s == nil || !s.operations.enter() {
		return
	}
	defer s.operations.leave()

	if userID <= 0 {
		return
	}
	keys, err := s.apiKeyRepo.ListKeysByUserID(ctx, userID)
	// Enumeration failure leaves the cache entries intact for this attempt;
	// database-triggered outbox delivery or expiration supplies later invalidation.
	if err != nil {
		return
	}
	s.KeyDeleteAuthCacheByKeys(ctx, keys)
}

// InvalidateAuthCacheByGroupID invalidates credentials bound directly or
// through a composite mapping to groupID. Nil or stopping services and
// nonpositive IDs do nothing; failed repository enumeration skips the attempt
// for this call.
func (s *APIKeyService) InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64) {
	if s == nil || !s.operations.enter() {
		return
	}
	defer s.operations.leave()

	if groupID <= 0 {
		return
	}
	keys, err := s.apiKeyRepo.ListKeysByGroupID(ctx, groupID)
	// Enumeration failure leaves the cache entries intact for this attempt;
	// database-triggered outbox delivery or expiration supplies later invalidation.
	if err != nil {
		return
	}
	s.KeyDeleteAuthCacheByKeys(ctx, keys)
}

// KeyDeleteAuthCacheByKeys invalidates each nonempty credential's local and
// shared cached identity. Empty input does nothing; direct callers hold service
// operation admission or otherwise coordinate cache lifetime before calling
// this helper.
func (s *APIKeyService) KeyDeleteAuthCacheByKeys(ctx context.Context, keys []string) {
	if len(keys) == 0 {
		return
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		s.KeyDeleteAuthCache(ctx, s.KeyAuthCacheKey(key))
	}
}
