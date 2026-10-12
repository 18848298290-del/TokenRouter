package apikey

import (
	"context"
	"fmt"
	"time"
)

// Credential rotation replaces the secret on the existing Key record so
// subsequent clients must use the returned value. The storage compare against
// the old credential prevents a racing replacement from winning twice, and
// successful persistence is followed by invalidation of both old and generated
// cache keys.
//
// Managed or differently owned records appear missing to user callers. Team
// keys first hydrate the assigned member context according to the existing team
// rules. Cleanup uses a bounded context detached from client cancellation after
// the write, while database triggers provide durable invalidation delivery when
// the direct cache calls fail.

// RotateCredential generates and persists a replacement credential for an owned
// user-visible Key. Missing, managed or differently owned records return a
// missing-key error, and eligible team records first load their current
// context.
//
// Storage compares the supplied old credential before changing the same record,
// retaining its configuration and usage fields. Failed writes return before
// cache invalidation. Successful writes invalidate both secrets with a detached
// five-second cleanup budget, so client cancellation after persistence does not
// discard that attempt; cache failures rely on the separate durable outbox
// protocol.
// @project-doc docs/domains/identity_and_tenancy.md#api_key_rotation
func (s *APIKeyService) RotateCredential(ctx context.Context, id, userID int64) (*APIKey, error) {
	key, err := s.apiKeyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	if key == nil || key.ManagedBy != nil || key.UserID != userID {
		return nil, ErrAPIKeyNotFound
	}
	if key.TeamID != nil {
		key, err = s.KeyHydrateTeamAPIKey(ctx, key, nil)
		if err != nil {
			return nil, err
		}
	}

	oldKey := key.Key
	newKey, err := s.GenerateKey()
	if err != nil {
		return nil, err
	}
	key.Key = newKey
	if err := s.apiKeyRepo.RotateCredential(ctx, key, oldKey); err != nil {
		return nil, fmt.Errorf("rotate api key credential: %w", err)
	}

	// The write can commit before the client disconnects. Use a separate
	// cleanup budget so that cancellation does not skip invalidating either secret.
	invalidationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.InvalidateAuthCacheByKey(invalidationCtx, oldKey)
	s.InvalidateAuthCacheByKey(invalidationCtx, newKey)
	return key, nil
}
