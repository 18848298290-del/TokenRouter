package identity

import (
	"context"
	"errors"
	"time"
)

// ErrRefreshTokenNotFound is returned when a refresh token is not found in cache.
// This is used to abstract away the underlying cache implementation (e.g., redis.Nil).
var ErrRefreshTokenNotFound = errors.New("refresh token not found")

// RefreshTokenData is the cached state for a refresh token.
type RefreshTokenData struct {
	UserID       int64     `json:"user_id"`
	TokenVersion int64     `json:"token_version"`          // Compared with the current user fingerprint to detect revoked credentials.
	FamilyID     string    `json:"family_id"`              // Session family used for rotation and family-wide revocation.
	BindingHash  string    `json:"binding_hash,omitempty"` // IP and User-Agent hash checked when session binding is enabled.
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// RefreshTokenCache stores refresh-token state and indexes for JWT renewal.
// Consumption supports one-time rotation; indexes support user and family revocation.
//
// Redis key formats:
//   - refresh_token:{token_hash}     -> RefreshTokenData (JSON)
//   - user_refresh_tokens:{user_id}  -> Set<token_hash>
//   - token_family:{family_id}       -> Set<token_hash>
type RefreshTokenCache interface {
	// StoreRefreshToken stores token metadata with the supplied TTL.
	// tokenHash is the SHA-256 digest; the raw token is not stored.
	// data contains the user, family, binding, and expiration metadata.
	// ttl is the cache lifetime.
	StoreRefreshToken(ctx context.Context, tokenHash string, data *RefreshTokenData, ttl time.Duration) error

	// GetRefreshToken retrieves token metadata.
	// It returns data and a nil error when the token exists.
	// A missing token returns ErrRefreshTokenNotFound.
	// Other storage or decoding failures are returned to the caller.
	GetRefreshToken(ctx context.Context, tokenHash string) (*RefreshTokenData, error)

	// ConsumeRefreshToken atomically removes a validated token; only the successful remover may issue its successor.
	// A missing token returns false. Storage failures return an error and do not authorize renewal.
	ConsumeRefreshToken(ctx context.Context, tokenHash string) (bool, error)

	// DeleteRefreshToken removes one refresh token.
	// Use it for logout and expiry cleanup; rotation requires the result from ConsumeRefreshToken.
	DeleteRefreshToken(ctx context.Context, tokenHash string) error

	// DeleteUserRefreshTokens deletes all tokens indexed for a user.
	// Use it for password changes or logout across devices.
	DeleteUserRefreshTokens(ctx context.Context, userID int64) error

	// DeleteTokenFamily removes all tokens indexed for a session family.
	// Use it to revoke a session chain after detecting misuse.
	DeleteTokenFamily(ctx context.Context, familyID string) error

	// AddToUserTokenSet adds a token hash to the user index.
	// The index supports revoking the user's refresh tokens.
	AddToUserTokenSet(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) error

	// AddToFamilyTokenSet adds a token hash to the family index.
	// The index groups tokens issued for one login session.
	AddToFamilyTokenSet(ctx context.Context, familyID string, tokenHash string, ttl time.Duration) error

	// GetUserTokenHashes returns the hashes indexed for a user.
	// The hashes identify tokens for bulk revocation.
	GetUserTokenHashes(ctx context.Context, userID int64) ([]string, error)

	// GetFamilyTokenHashes returns the hashes indexed for a session family.
	// The hashes identify tokens for family-wide revocation.
	GetFamilyTokenHashes(ctx context.Context, familyID string) ([]string, error)

	// IsTokenInFamily reports whether the family index contains the token hash.
	// Membership reflects the cached family index.
	IsTokenInFamily(ctx context.Context, familyID string, tokenHash string) (bool, error)
}
