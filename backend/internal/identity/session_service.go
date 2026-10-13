package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Access credentials carry signed account claims, while refresh credentials
// carry random text whose digest addresses cache state. Pair issuance uses the
// refresh family as access-token sid, allowing family cleanup and administrator
// step-up to refer to the same session. Access-only issuance creates a separate
// sid and does not store a refresh credential for it.
//
// Refresh-pair renewal loads current account state and checks its credential
// fingerprint before claiming the cached token once. Only successful
// consumption permits a successor; issuance failure afterward leaves the
// consumed token unavailable. User/family indexes support broader cleanup but
// can be incomplete after their separate writes fail. Access-token revocation
// continues to depend on current stored credentials and state, not an
// independent persisted version counter in the users table.

const (
	// MaxTokenLength bounds supplied JWT text before parsing, limiting the input
	// available to header and claims decoding on authentication requests.
	MaxTokenLength = 8192

	// RefreshTokenPrefix identifies random refresh credentials at renewal and
	// logout entry points before they are hashed for cache record lookup.
	RefreshTokenPrefix = "rt_"
)

// JWTClaims carries signed account identity, credential fingerprint and
// optional session binding.
type JWTClaims struct {
	UserID int64  `json:"user_id"` // issued local account identifier
	Email  string `json:"email"`   // issued account email text
	Role   string `json:"role"`    // issued global account role
	// TokenVersion carries the resolved credential fingerprint at issuance.
	// Authentication compares it with current user data to detect email or
	// password changes.
	TokenVersion int64 `json:"token_version"`
	// SessionID identifies the refresh family for pair issuance and its step-up
	// grants. Access-only issuance creates its own sid without storing refresh
	// state.
	SessionID string `json:"sid,omitempty"`
	// BindingHash carries the context fingerprint at issuance. Empty means the
	// token has no stored binding; renewal compares present hashes when settings
	// enable it.
	BindingHash          string `json:"bnd,omitempty"`
	jwt.RegisteredClaims        // standard JWT timestamp claims
}

// TokenPair is the client result containing access and refresh credentials with
// access lifetime.
type TokenPair struct {
	AccessToken  string `json:"access_token"`  // signed access credential text
	RefreshToken string `json:"refresh_token"` // raw refresh credential text
	ExpiresIn    int    `json:"expires_in"`    // Access-token lifetime in seconds.
}

// TokenPairWithUser carries renewed credentials and current user role for
// backend-mode checks.
type TokenPairWithUser struct {
	TokenPair        // renewed client credential result
	UserRole  string // current global account role
}

// SessionOptions holds token signing and lifetime inputs retained by the
// session service.
type SessionOptions struct {
	// Now is read by individual session operations. Nil selects the wall clock
	// through SessionService.now; callers retain any injected closure until
	// service use ends.
	Now func() time.Time
	// Secret supplies raw HMAC key bytes for signing and validation. The core
	// retains this string as provided; startup validation belongs to its
	// configuration caller.
	Secret string
	// ExpireHour and AccessTokenExpireMinutes select one access lifetime. A
	// positive minute value wins; otherwise the hour value is used directly by
	// signing and by the seconds reported to clients.
	ExpireHour               int // hours
	AccessTokenExpireMinutes int // minutes
	// RefreshTokenExpireDays becomes both the stored expiry and cache duration.
	// Callers supply validated startup lifetime values; this core converts the
	// value directly and does not add a positive-duration default.
	RefreshTokenExpireDays int
}

// SessionUserReader supplies fresh account state for access and refresh-token
// renewal.
type SessionUserReader interface {
	// GetByID returns the current account or an error for the selected identifier.
	// Missing accounts use ErrUserNotFound, and nil error requires a non-nil user
	// because renewal reads its active state and credential fingerprint directly.
	GetByID(context.Context, int64) (*User, error)
}

// SessionSettings supplies the current client-binding feature decision for
// renewal.
type SessionSettings interface { /*
		IsSessionBindingEnabled reports whether renewal compares stored and current
		client fingerprints. RuntimeSettings returns false after a setting read fails,
		leaving this check disabled for that particular feature query.
	*/IsSessionBindingEnabled(context.Context) bool
}

// SessionService signs access claims and rotates cached refresh credentials
// after account checks.
type SessionService struct {
	options SessionOptions // retained signing and lifetime options
	// userRepo reloads active state and credential data before renewal. Configure
	// it for renewal calls; access signing reads the supplied user directly
	// instead.
	userRepo SessionUserReader
	// refreshTokenCache stores pair credentials and supplies their consumption.
	// Access-only signing can operate with nil; pair generation and renewal need
	// cache state.
	refreshTokenCache RefreshTokenCache
	// settingService controls binding comparisons during renewal. Nil skips the
	// comparison, while issuance still records any fingerprint supplied by the
	// context.
	settingService SessionSettings
	observer       Observer // session operation diagnostic callback
}

// NewSessionService retains token options and the supplied renewal
// dependencies. Options are copied by value, including the clock function
// reference; callers configure user and cache readers before using operations
// that require them.
func NewSessionService(options SessionOptions, users SessionUserReader, cache RefreshTokenCache, settings SessionSettings, logf LogFunc) *SessionService {
	return &SessionService{options: options, userRepo: users, refreshTokenCache: cache, settingService: settings, observer: Observer{Log: logf}}
}

// ValidateToken checks JWT text with the configured HMAC secret and returns
// parsed claims. It accepts HS256, HS384 and HS512, rejects oversized input
// before parsing, and leaves current account state and credential-version
// checks to callers admitting authenticated requests.
//
// The parser checks present exp and nbf claims after signature verification.
// Errors containing expiration return claims with ErrTokenExpired, including
// expiration joined with another claims-validation error; other parser failures
// become ErrInvalidToken. This compatibility result supports access-token
// renewal and does not imply that expiration was the token's sole validation
// failure.
func (s *SessionService) ValidateToken(tokenString string) (*JWTClaims, error) {
	if len(tokenString) > MaxTokenLength {
		return nil, ErrTokenTooLarge
	}

	// Restrict accepted signing methods to HMAC algorithms.
	parser := jwt.NewParser(jwt.WithTimeFunc(s.now), jwt.WithValidMethods([]string{
		jwt.SigningMethodHS256.Name,
		jwt.SigningMethodHS384.Name,
		jwt.SigningMethodHS512.Name,
	}))

	// Keep exp and nbf validation enabled.
	token, err := parser.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(s.options.Secret), nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			// The library verifies signatures before claim validation. Preserve its
			// parsed claims for the expiry compatibility result even when that
			// validation error also contains another claim failure such as not-before.
			if claims, ok := token.Claims.(*JWTClaims); ok {
				return claims, ErrTokenExpired
			}
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, ErrInvalidToken
}

// GenerateToken signs an access-only credential with a new random session ID.
// The supplied user must be non-nil; its current fields and the context's
// binding hash become claims. This path stores no refresh record for that
// generated sid.
func (s *SessionService) GenerateToken(ctx context.Context, user *User) (string, error) {
	sessionID, err := RandomHexString(8)
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return s.GenerateAccessToken(user, sessionID, SessionBindingHashFromContext(ctx))
}

// GenerateAccessToken signs the supplied non-nil user's fields as an HS256 JWT.
// sessionID and bindingHash are copied as provided, and the credential version
// is resolved from the user. Positive minute lifetime takes precedence over the
// retained hour lifetime; this signing step performs no account-status lookup.
func (s *SessionService) GenerateAccessToken(user *User, sessionID, bindingHash string) (string, error) {
	now := s.now()
	var expiresAt time.Time
	if s.options.AccessTokenExpireMinutes > 0 {
		expiresAt = now.Add(time.Duration(s.options.AccessTokenExpireMinutes) * time.Minute)
	} else {
		// A nonpositive minute setting leaves the startup hour lifetime
		// authoritative, keeping signing aligned with the seconds returned by
		// GetAccessTokenExpiresIn.
		expiresAt = now.Add(time.Duration(s.options.ExpireHour) * time.Hour)
	}

	claims := &JWTClaims{
		UserID:       user.ID,
		Email:        user.Email,
		Role:         user.Role,
		TokenVersion: ResolvedTokenVersion(user),
		SessionID:    sessionID,
		BindingHash:  bindingHash,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(s.options.Secret))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	return tokenString, nil
}

// GetAccessTokenExpiresIn reports the selected access lifetime in whole
// seconds. Positive AccessTokenExpireMinutes wins; otherwise ExpireHour is
// converted, so this client-facing value follows the same lifetime choice used
// when signing.
func (s *SessionService) GetAccessTokenExpiresIn() int {
	if s.options.AccessTokenExpireMinutes > 0 {
		// Convert configured minutes to the seconds clients use for renewal.
		return s.options.AccessTokenExpireMinutes * 60
	}
	// Convert the legacy hour lifetime to the same client unit.
	return s.options.ExpireHour * 3600
}

// HashPassword returns a bcrypt hash of the supplied password bytes at the
// library's default cost. A password longer than seventy-two bytes returns
// ErrPasswordTooLong before salt generation; accepted passwords are hashed as
// supplied for later comparison.
func (s *SessionService) HashPassword(password string) (string, error) {
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}

// CheckPassword reports whether bcrypt accepts the supplied password against
// its hash. Mismatches, malformed hashes, and other bcrypt comparison errors
// produce false, so authentication receives one rejection decision.
func (s *SessionService) CheckPassword(password, hashedPassword string) bool {
	// A malformed stored hash can fail before bcrypt compares the password.
	// Reject that error so unusable credential data cannot authenticate an account.
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}

// RefreshToken renews an access-only JWT after reloading its current account.
// It accepts ValidateToken's expiration result, then requires active status and
// the current resolved credential fingerprint before signing a replacement.
//
// Present stored and current bindings are compared when the binding feature is
// enabled; a mismatch attempts family cleanup and rejects renewal even if
// cleanup fails. The replacement uses GenerateToken, so it receives a new sid
// and the current context fingerprint rather than continuing a cached refresh
// family. This legacy renewal path does not consume refresh-token cache state.
func (s *SessionService) RefreshToken(ctx context.Context, oldTokenString string) (string, error) {
	// Accept expired access tokens for this renewal path.
	claims, err := s.ValidateToken(oldTokenString)
	if err != nil && !errors.Is(err, ErrTokenExpired) {
		return "", err
	}

	// Reload the user so renewal uses current credentials and status.
	user, err := s.userRepo.GetByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// Without a current account, these claims cannot authorize renewal, so
			// report the common invalid-token result for that missing user identity.
			return "", ErrInvalidToken
		}
		// This read failure prevents account validation. Report temporary
		// unavailability while leaving the supplied access credential itself
		// untouched.
		s.observer.Printf("service.auth", "[Auth] Database error refreshing token: %v", err)
		return "", ErrServiceUnavailable
	}

	if !user.IsActive() {
		return "", ErrUserNotActive
	}

	// Reject tokens issued against a different credential fingerprint.
	if claims.TokenVersion != ResolvedTokenVersion(user) {
		return "", ErrTokenRevoked
	}

	// Compare only when both fingerprints are available for this enabled check. A
	// mismatch rejects renewal after attempting family cleanup, whose failure
	// cannot turn the mismatched credential into an accepted session.
	if s.settingService != nil && s.settingService.IsSessionBindingEnabled(ctx) && claims.BindingHash != "" {
		if current := SessionBindingHashFromContext(ctx); current != "" && current != claims.BindingHash {
			_ = s.RevokeSessionFamily(ctx, claims.SessionID)
			return "", ErrSessionBindingMismatch
		}
	}

	return s.GenerateToken(ctx, user)
}

// GenerateTokenPair signs an access credential and stores a matching refresh
// credential for a non-nil user. A configured cache is required; empty familyID
// generates a new family, while supplied family text continues that session.
//
// Access sid and refresh FamilyID receive the same value so family cleanup and
// step-up identify the pair together. Access signing completes before refresh
// persistence, and the pair is returned only after both succeed. Refresh index
// writes can still fail independently inside GenerateRefreshToken, whose saved
// credential is returned after those diagnostic-only failures.
func (s *SessionService) GenerateTokenPair(ctx context.Context, user *User, familyID string) (*TokenPair, error) {
	if s.refreshTokenCache == nil {
		return nil, errors.New("refresh token cache not configured")
	}

	// Share the chosen family across the JWT sid and refresh metadata, so their
	// revocation and step-up consumers refer to the same issued session identity.
	if familyID == "" {
		familyBytes := make([]byte, 16)
		if _, err := rand.Read(familyBytes); err != nil {
			return nil, fmt.Errorf("generate family id: %w", err)
		}
		familyID = hex.EncodeToString(familyBytes)
	}

	accessToken, err := s.GenerateAccessToken(user, familyID, SessionBindingHashFromContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", err)
	}

	refreshToken, err := s.GenerateRefreshToken(ctx, user, familyID)
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    s.GetAccessTokenExpiresIn(),
	}, nil
}

// GenerateRefreshToken stores a random refresh credential's digest and returns
// its raw text. Callers provide a non-nil user, configured cache and validated
// lifetime options; an empty family ID generates a new session family.
//
// Metadata records one clock observation and its derived expiry before the user
// and family index writes are attempted. Record storage errors prevent
// returning a credential, while index errors are diagnosed and the saved token
// still succeeds. Those separate writes can leave a valid record outside a
// revocation index, so index membership is a cleanup aid rather than a complete
// token ledger.
func (s *SessionService) GenerateRefreshToken(ctx context.Context, user *User, familyID string) (string, error) {
	// Thirty-two random bytes become sixty-four hexadecimal refresh credential
	// characters.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	rawToken := RefreshTokenPrefix + hex.EncodeToString(tokenBytes)

	// Use the digest as the storage key; return the raw credential to the caller.
	tokenHash := HashToken(rawToken)

	if familyID == "" {
		familyBytes := make([]byte, 16)
		if _, err := rand.Read(familyBytes); err != nil {
			return "", fmt.Errorf("generate family id: %w", err)
		}
		familyID = hex.EncodeToString(familyBytes)
	}

	now := s.now()
	// Convert configured days into the duration used by expiry metadata and cache.
	ttl := time.Duration(s.options.RefreshTokenExpireDays) * 24 * time.Hour

	data := &RefreshTokenData{
		UserID:       user.ID,
		TokenVersion: ResolvedTokenVersion(user),
		FamilyID:     familyID,
		BindingHash:  SessionBindingHashFromContext(ctx),
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}

	// Save the credential record before indexing it, so a later index failure
	// cannot prevent this stored token from being returned for ordinary renewal.
	if err := s.refreshTokenCache.StoreRefreshToken(ctx, tokenHash, data, ttl); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}

	// Add the saved digest to user-wide cleanup candidates. Index errors leave
	// membership unconfirmed and are diagnosed while the saved credential remains
	// usable.
	if err := s.refreshTokenCache.AddToUserTokenSet(ctx, user.ID, tokenHash, ttl); err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to add token to user set: %v", err)
	}

	// Add the same saved digest to family cleanup candidates separately, so an
	// error here affects that index independently of the completed record and user
	// index.
	if err := s.refreshTokenCache.AddToFamilyTokenSet(ctx, familyID, tokenHash, ttl); err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to add token to family set: %v", err)
	}

	return rawToken, nil
}

// RefreshTokenPair validates a stored refresh credential and rotates its pair.
// It requires the prefix, readable metadata, a nonexpired stored timestamp and
// an active current account whose credential fingerprint still matches. Binding
// checks apply when enabled and both saved and current hashes are present.
//
// Validation completes before ConsumeRefreshToken claims the credential. False
// consumption returns ErrRefreshTokenInvalid; a cache error returns service
// unavailable and authorizes no successor. A successful claim permits issuance
// in the same family, and an issuance error leaves the old record consumed.
// Missing users, inactive accounts or stale fingerprints attempt family cleanup
// before rejection, with cleanup failures leaving that rejection unchanged.
func (s *SessionService) RefreshTokenPair(ctx context.Context, refreshToken string) (*TokenPairWithUser, error) {
	if s.refreshTokenCache == nil {
		return nil, ErrRefreshTokenInvalid
	}

	if !strings.HasPrefix(refreshToken, RefreshTokenPrefix) {
		return nil, ErrRefreshTokenInvalid
	}

	tokenHash := HashToken(refreshToken)

	data, err := s.refreshTokenCache.GetRefreshToken(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			// An absent digest cannot authorize rotation and may already have been
			// consumed or expired; expose the common invalid-credential result before
			// issuance.
			s.observer.Printf("service.auth", "[Auth] Refresh token not found, possible reuse attack")
			return nil, ErrRefreshTokenInvalid
		}
		// Unreadable metadata prevents validation before any consumption claim, so
		// this storage failure reports service unavailability without authorizing a
		// successor.
		s.observer.Printf("service.auth", "[Auth] Error getting refresh token: %v", err)
		return nil, ErrServiceUnavailable
	}

	// Check the stored expiration time as well as the cache TTL.
	if s.now().After(data.ExpiresAt) {
		// Expired metadata already rejects this renewal; deleting the cached record
		// is cleanup whose failure cannot authorize the credential or alter that
		// error.
		_ = s.refreshTokenCache.DeleteRefreshToken(ctx, tokenHash)
		return nil, ErrRefreshTokenExpired
	}

	// Load current user state before consuming the credential.
	user, err := s.userRepo.GetByID(ctx, data.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// A missing account makes this family ineligible for renewal. Attempt its
			// indexed cleanup while retaining the invalid-credential outcome if deletion
			// fails.
			_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
			return nil, ErrRefreshTokenInvalid
		}
		// Current account validation is unavailable. Consumption has not begun, so
		// retain that phase ordering while reporting this temporary repository
		// failure.
		s.observer.Printf("service.auth", "[Auth] Database error getting user for token refresh: %v", err)
		return nil, ErrServiceUnavailable
	}

	if !user.IsActive() {
		// Inactive account state rejects renewal independently of cache cleanup;
		// attempt family deletion while retaining that rejection if cleanup cannot
		// complete.
		_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
		return nil, ErrUserNotActive
	}

	// Compare the current credential fingerprint to detect password or email
	// changes.
	if data.TokenVersion != ResolvedTokenVersion(user) {
		// Stale credential state rejects renewal before consumption. Attempt family
		// cleanup while leaving that rejection authoritative after a deletion
		// failure.
		_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
		return nil, ErrTokenRevoked
	}

	// Compare present hashes for this enabled binding check; absent hashes let
	// renewal acquire current context data. Mismatch cleanup is best effort, while
	// the returned mismatch still prevents issuing a successor.
	if s.settingService != nil && s.settingService.IsSessionBindingEnabled(ctx) && data.BindingHash != "" {
		if current := SessionBindingHashFromContext(ctx); current != "" && current != data.BindingHash {
			_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
			s.observer.Printf("service.auth", "[Auth] Session binding mismatch on refresh for user %d, family revoked", data.UserID)
			return nil, ErrSessionBindingMismatch
		}
	}

	// Claim only after account and binding checks, leaving credentials available
	// after temporary validation failures. Only the successful remover can
	// authorize a successor after competing requests have read the same record.
	consumed, err := s.refreshTokenCache.ConsumeRefreshToken(ctx, tokenHash)
	if err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to delete old refresh token: %v", err)
		return nil, ErrServiceUnavailable
	}
	if !consumed {
		return nil, ErrRefreshTokenInvalid
	}

	// Continue the claimed family for the new pair. Failure here leaves the old
	// credential unavailable because consumption completed before successor
	// generation began.
	pair, err := s.GenerateTokenPair(ctx, user, data.FamilyID)
	if err != nil {
		return nil, err
	}
	return &TokenPairWithUser{
		TokenPair: *pair,
		UserRole:  user.Role,
	}, nil
}

// RevokeRefreshToken removes one prefixed refresh credential by its digest. An
// unset cache returns nil, while a configured cache requires the refresh prefix
// and returns its deletion result; revocation does not use the rotation claim.
func (s *SessionService) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	if s.refreshTokenCache == nil {
		return nil // No-op if cache not configured
	}
	if !strings.HasPrefix(refreshToken, RefreshTokenPrefix) {
		return ErrRefreshTokenInvalid
	}

	tokenHash := HashToken(refreshToken)
	return s.refreshTokenCache.DeleteRefreshToken(ctx, tokenHash)
}

// RevokeSessionFamily removes refresh records named by the selected family
// index. A missing cache or empty family is a no-op; other deletion failures
// are returned. Index completeness and concurrent additions determine which
// records the cache cleanup can actually observe.
func (s *SessionService) RevokeSessionFamily(ctx context.Context, familyID string) error {
	if s.refreshTokenCache == nil || familyID == "" {
		return nil
	}
	return s.refreshTokenCache.DeleteTokenFamily(ctx, familyID)
}

// RevokeAllUserSessions removes refresh records indexed for the selected
// account. An unset cache is a successful no-op; configured-cache errors are
// returned, and the operation's coverage follows the current user index
// maintained by issuance.
func (s *SessionService) RevokeAllUserSessions(ctx context.Context, userID int64) error {
	if s.refreshTokenCache == nil {
		return nil // No-op if cache not configured
	}
	return s.refreshTokenCache.DeleteUserRefreshTokens(ctx, userID)
}

// RevokeAllUserTokens verifies that the user exists and attempts to revoke all
// refresh sessions.
//
// Access-token versions are derived from email and password_hash; users has no
// token_version column. Changing those stored credentials invalidates access
// tokens through ResolvedTokenVersion. Updating the in-memory TokenVersion
// alone cannot invalidate persisted credentials. This method leaves
// access-token validity to those credential checks. Refresh-session cleanup
// failures are logged and do not change the returned result.
func (s *SessionService) RevokeAllUserTokens(ctx context.Context, userID int64) error {
	if _, err := s.userRepo.GetByID(ctx, userID); err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	// Account existence succeeded, but refresh cleanup remains best effort;
	// diagnose its failure while access validity continues to follow stored
	// credentials.
	if err := s.RevokeAllUserSessions(ctx, userID); err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to revoke refresh sessions after token invalidation for user %d: %v", userID, err)
	}
	return nil
}

// HashToken returns the lowercase hexadecimal SHA-256 digest of supplied
// credential text.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// ResolvedTokenVersion returns the credential version compared by session
// checks. Nil users produce zero, while TokenVersionResolved selects the
// already resolved value directly. Other users combine their supplied seed with
// a fingerprint.
//
// The fingerprint hashes lowercased trimmed email, a newline and the exact
// password hash, retaining the first eight bytes with the sign bit removed. XOR
// with TokenVersion preserves any supplied in-memory seed. The users table has
// no dedicated stored version column; callers marking a resolved snapshot keep
// its version consistent with that snapshot's credential fields.
func ResolvedTokenVersion(user *User) int64 {
	if user == nil {
		return 0
	}
	if user.TokenVersionResolved {
		return user.TokenVersion
	}

	material := strings.ToLower(strings.TrimSpace(user.Email)) + "\n" + user.PasswordHash
	sum := sha256.Sum256([]byte(material))
	// Keep the fingerprint portion nonnegative before combining it with the
	// supplied seed, using the same leading digest bytes for each resolved
	// snapshot.
	fingerprint := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	return user.TokenVersion ^ fingerprint
}

// RandomHexString returns lowercase hex for the requested number of random
// bytes with nil error. Nonpositive lengths select sixteen bytes, and callers
// receive twice the byte count as text characters. crypto/rand.Read terminates
// the process when its io.ReadFull operation reports an error.
func RandomHexString(byteLength int) (string, error) {
	if byteLength <= 0 {
		byteLength = 16
	}
	buf := make([]byte, byteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
