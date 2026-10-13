package identity

import (
	"context"
	"errors"
	"time"
)

// Refresh credential records are addressed by digest, while user and family
// sets supply candidates for wider revocation. SessionService saves the record
// before adding those indexes, so failed index writes can leave a usable record
// outside their membership. Index reads can also retain digests whose records
// have already been consumed or removed.
//
// Rotation depends on the cache reporting one successful record removal after
// service validation. Broader revocation follows the index membership observed
// by the adapter and can miss concurrent additions or unindexed records. The
// interface therefore separates credential consumption, record deletion and
// index management so callers can apply their distinct renewal and cleanup
// rules.

// ErrRefreshTokenNotFound identifies an absent credential record for session
// renewal and keeps Redis-specific absence errors outside the identity core.
var ErrRefreshTokenNotFound = errors.New("refresh token not found")

// RefreshTokenData holds account, family, binding and expiry observations saved
// at issuance.
type RefreshTokenData struct {
	UserID int64 `json:"user_id"` // issuing local account identifier
	// TokenVersion stores the resolved credential fingerprint at issuance. Renewal
	// compares it with the current account before claiming this record.
	TokenVersion int64 `json:"token_version"`
	// FamilyID is shared with the pair's JWT sid and retained across renewal.
	// Family revocation uses it to find the separate indexed credential digests.
	FamilyID string `json:"family_id"`
	// BindingHash saves the context fingerprint. Renewal compares it when binding
	// is enabled and both this value and the current fingerprint are present.
	BindingHash string `json:"binding_hash,omitempty"`
	// CreatedAt and ExpiresAt use the same issuance clock observation, with the
	// selected cache duration added for expiry. Renewal rejects times after
	// ExpiresAt in addition to whatever expiry the cache applies to its record.
	CreatedAt time.Time `json:"created_at"` // credential issuance timestamp
	ExpiresAt time.Time `json:"expires_at"` // credential expiry timestamp
}

// RefreshTokenCache stores refresh-token state and indexes for JWT renewal.
// Consumption supports one-time rotation; indexes support user and family
// revocation.
//
// Redis key formats:
//
//   - refresh_token:{token_hash} -> RefreshTokenData (JSON).
//   - user_refresh_tokens:{user_id} -> Set<token_hash>.
//   - token_family:{family_id} -> Set<token_hash>.
type RefreshTokenCache interface {
	// StoreRefreshToken saves metadata under the SHA-256 credential digest.
	// Callers select ttl and store the record before its revocation indexes; the
	// Redis adapter serializes JSON and returns encoding or write failures.
	StoreRefreshToken(ctx context.Context, tokenHash string, data *RefreshTokenData, ttl time.Duration) error

	// GetRefreshToken reads the credential record for its supplied digest. The
	// Redis adapter decodes a fresh value and maps absence to
	// ErrRefreshTokenNotFound; other read or JSON failures remain errors. Nil
	// error requires non-nil data for renewal checks.
	GetRefreshToken(ctx context.Context, tokenHash string) (*RefreshTokenData, error)

	// ConsumeRefreshToken reports whether it removed the validated credential
	// once. Only a true result with nil error permits a successor; missing records
	// return false, and storage errors cannot authorize renewal even after
	// uncertain removal.
	ConsumeRefreshToken(ctx context.Context, tokenHash string) (bool, error)

	// DeleteRefreshToken removes one digest-addressed credential for cleanup or
	// logout. The Redis adapter accepts an absent record and retains its index
	// memberships; rotation instead requires the winning result from
	// ConsumeRefreshToken.
	DeleteRefreshToken(ctx context.Context, tokenHash string) error

	// DeleteUserRefreshTokens removes records named by the observed user index.
	// The Redis adapter reads membership before pipelining deletes, so concurrent
	// additions or incomplete indexes can escape cleanup and failures can follow
	// partial deletion.
	DeleteUserRefreshTokens(ctx context.Context, userID int64) error

	// DeleteTokenFamily removes records named by the observed session-family
	// index. The Redis adapter snapshots membership before deletion commands,
	// leaving concurrent additions and unindexed credentials outside that observed
	// cleanup scope.
	DeleteTokenFamily(ctx context.Context, familyID string) error

	// AddToUserTokenSet adds a digest to user-wide revocation candidates and
	// refreshes index lifetime. The Redis adapter queues membership before expiry
	// in a regular pipeline; errors can leave either operation only partly
	// applied.
	AddToUserTokenSet(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) error

	// AddToFamilyTokenSet adds a digest to its family's revocation candidates and
	// refreshes index lifetime. The Redis adapter applies ttl to the whole set,
	// and pipeline failures can leave its membership or expiry incomplete.
	AddToFamilyTokenSet(ctx context.Context, familyID string, tokenHash string, ttl time.Duration) error

	// GetUserTokenHashes returns the currently indexed credential digests for a
	// user. An absent Redis set returns empty; consumed credentials can retain
	// membership, so the values are deletion candidates rather than proof of valid
	// records.
	GetUserTokenHashes(ctx context.Context, userID int64) ([]string, error)

	// GetFamilyTokenHashes returns the credential digests currently indexed for a
	// family. An absent Redis set returns empty; membership can outlive individual
	// record consumption, and a returned digest still requires its own record
	// lookup for renewal.
	GetFamilyTokenHashes(ctx context.Context, familyID string) ([]string, error)

	// IsTokenInFamily reports whether the family index contains the supplied
	// digest. The Redis adapter returns false for an absent set. Membership alone
	// cannot establish that its separate credential record remains usable for
	// renewal.
	IsTokenInFamily(ctx context.Context, familyID string, tokenHash string) (bool, error)
}
