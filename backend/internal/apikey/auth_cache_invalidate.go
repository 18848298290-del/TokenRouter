package apikey

import (
	"context"
)

// InvalidateAuthCacheByKey invalidates authentication caches for a credential.
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

// InvalidateAuthCacheByUserID invalidates authentication caches for the user's keys.
func (s *APIKeyService) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	if s == nil || !s.operations.enter() {
		return
	}
	defer s.operations.leave()

	if userID <= 0 {
		return
	}
	keys, err := s.apiKeyRepo.ListKeysByUserID(ctx, userID)
	if err != nil {
		return
	}
	s.KeyDeleteAuthCacheByKeys(ctx, keys)
}

// InvalidateAuthCacheByGroupID invalidates authentication caches for keys bound to the group.
func (s *APIKeyService) InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64) {
	if s == nil || !s.operations.enter() {
		return
	}
	defer s.operations.leave()

	if groupID <= 0 {
		return
	}
	keys, err := s.apiKeyRepo.ListKeysByGroupID(ctx, groupID)
	if err != nil {
		return
	}
	s.KeyDeleteAuthCacheByKeys(ctx, keys)
}

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
