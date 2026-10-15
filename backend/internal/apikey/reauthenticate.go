package apikey

import (
	"context"
)

// reauthenticate.go reloads authorization for a connection while checking that
// its credential and funding identities still match.
// A long-lived connection keeps its original credential and billing identity
// while later turns need current authorization. Reauthentication bypasses
// positive caches and compares the loaded record ID, creator, team, payer and
// funding selection with that original view. A replacement credential record or
// changed funding identity therefore requires reconnection before another
// consuming turn.
//
// Successful validation constructs a fresh Key view, recompiles IP rules,
// checks current expiry and quota, and resolves the connection's selected group
// against current payer access. The preceding admitted turn retains its prior
// view for settlement. Storage errors and lifecycle rejection stop the new
// turn; the gateway performs subscription, funds and protocol checks after this
// reload.

// Reauthenticate reloads a connection's credential from storage and returns a
// fresh currently authorized Key view. Nil or unusable previous identity
// reports a missing Key; stopping authentication rejects admission before
// lookup.
//
// The stored record must match the prior Key ID, creator and team, and identity
// checks must accept its user, IP and optional member allowance. Payer, funding
// mode or preferred subscription changes require reconnection. Current expiry
// and quota states reject new spending, and the connection's prior selected
// group is resolved again before the new view is returned.
func (s *APIKeyService) Reauthenticate(ctx context.Context, previous *APIKey, input AuthenticationInput) (*APIKey, error) {
	if s == nil || previous == nil || previous.User == nil || previous.Key == "" || len(previous.Key) > MaxAPIKeyCredentialBytes {
		return nil, ErrAPIKeyNotFound
	}
	if !s.operations.enter() {
		return nil, ErrAuthenticationStopped
	}
	defer s.operations.leave()

	key, err := s.KeyLookupAPIKeyForAuth(ctx, previous.Key)
	if err != nil {
		return nil, err
	}
	if key == nil || key.ID != previous.ID || key.UserID != previous.UserID || !sameKeyID(key.TeamID, previous.TeamID) {
		return nil, ErrAPIKeyNotFound
	}
	key = CopyAPIKey(key)
	key.Key = previous.Key
	s.KeyCompileAPIKeyIPRules(key)
	if _, err := s.authenticateKey(key, input); err != nil {
		return nil, err
	}
	// The connection retains billing identity for admitted turns. Reject a
	// changed payer or source before another turn can acquire spending resources.
	if key.User.ID != previous.User.ID || APIKeyEffectiveBillingMode(key) != APIKeyEffectiveBillingMode(previous) || !sameKeyID(key.PreferredSubscriptionID, previous.PreferredSubscriptionID) {
		return nil, ErrAPIKeyNotFound
	}
	if err := s.CheckAPIKeyQuotaAndExpiry(key); err != nil {
		return nil, err
	}
	if key.Status == StatusAPIKeyExpired {
		return nil, ErrAPIKeyExpired
	}
	if key.Status == StatusAPIKeyQuotaExhausted {
		return nil, ErrAPIKeyQuotaExhausted
	}
	// The selected group remains the connection target, while current scope
	// and payer checks prevent a stored binding change from granting a later turn.
	if previous.GroupID != nil {
		key, err = s.ResolveRuntimeGroup(ctx, key, *previous.GroupID)
		if err != nil {
			return nil, err
		}
	}
	return key, nil
}

// sameKeyID compares optional integer IDs by value, with two nil pointers equal
// and one nil unequal. Reauthenticate uses this for team and
// preferred-subscription identity across reloads even when pointer storage
// differs.
func sameKeyID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
