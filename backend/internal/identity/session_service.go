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

const (
	// MaxTokenLength bounds token input before parsing to limit oversized-header allocations.
	MaxTokenLength = 8192

	// RefreshTokenPrefix is the prefix for refresh tokens to distinguish them from access tokens.
	RefreshTokenPrefix = "rt_"
)

// JWTClaims contains access-token claims.
type JWTClaims struct {
	UserID       int64  `json:"user_id"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	TokenVersion int64  `json:"token_version"` // Used to invalidate tokens on password change
	// SessionID identifies the refresh-token family for session revocation and step-up grants.
	SessionID string `json:"sid,omitempty"`
	// BindingHash contains the IP/User-Agent fingerprint; an empty value represents an unbound token.
	BindingHash string `json:"bnd,omitempty"`
	jwt.RegisteredClaims
}

// TokenPair contains an access token and its refresh token.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // Access-token lifetime in seconds.
}

// TokenPairWithUser extends TokenPair with user role for backend mode checks.
type TokenPairWithUser struct {
	TokenPair
	UserRole string
}

// SessionOptions contains startup JWT settings; the session binding switch is read per request.
type SessionOptions struct {
	Now                      func() time.Time
	Secret                   string
	ExpireHour               int
	AccessTokenExpireMinutes int
	RefreshTokenExpireDays   int
}
type SessionUserReader interface {
	GetByID(context.Context, int64) (*User, error)
}
type SessionSettings interface{ IsSessionBindingEnabled(context.Context) bool }

// SessionService issues and renews tokens using the injected refresh-token cache.
type SessionService struct {
	options           SessionOptions
	userRepo          SessionUserReader
	refreshTokenCache RefreshTokenCache
	settingService    SessionSettings
	observer          Observer
}

func NewSessionService(options SessionOptions, users SessionUserReader, cache RefreshTokenCache, settings SessionSettings, logf LogFunc) *SessionService {
	return &SessionService{options: options, userRepo: users, refreshTokenCache: cache, settingService: settings, observer: Observer{Log: logf}}
}

// ValidateToken validates a JWT and returns its claims; expired tokens return claims with ErrTokenExpired.
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
			// Return parsed claims with the expiration error for callers that support renewal.
			// The JWT parser populates claims even when expiration validation fails.
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

// GenerateToken issues a JWT access token with a new session ID.
// A positive AccessTokenExpireMinutes takes precedence over ExpireHour.
// The token uses the context fingerprint when present and is otherwise unbound.
func (s *SessionService) GenerateToken(ctx context.Context, user *User) (string, error) {
	sessionID, err := RandomHexString(8)
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return s.GenerateAccessToken(user, sessionID, SessionBindingHashFromContext(ctx))
}

// GenerateAccessToken signs an access token with the supplied session ID and binding hash.
func (s *SessionService) GenerateAccessToken(user *User, sessionID, bindingHash string) (string, error) {
	now := s.now()
	var expiresAt time.Time
	if s.options.AccessTokenExpireMinutes > 0 {
		expiresAt = now.Add(time.Duration(s.options.AccessTokenExpireMinutes) * time.Minute)
	} else {
		// Use the legacy hour-based lifetime when no positive minute-based value is configured.
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

// GetAccessTokenExpiresIn returns the configured access-token lifetime in seconds.
// Clients use this duration to schedule renewal.
func (s *SessionService) GetAccessTokenExpiresIn() int {
	if s.options.AccessTokenExpireMinutes > 0 {
		return s.options.AccessTokenExpireMinutes * 60
	}
	return s.options.ExpireHour * 3600
}

// HashPassword hashes a password with bcrypt at the default cost.
func (s *SessionService) HashPassword(password string) (string, error) {
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}

// CheckPassword reports whether a password matches its bcrypt hash.
func (s *SessionService) CheckPassword(password, hashedPassword string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}

// RefreshToken renews an access token after checking the current user and credential fingerprint.
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
			return "", ErrInvalidToken
		}
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

	// When both fingerprints are present, a mismatch revokes the family and rejects renewal.
	if s.settingService != nil && s.settingService.IsSessionBindingEnabled(ctx) && claims.BindingHash != "" {
		if current := SessionBindingHashFromContext(ctx); current != "" && current != claims.BindingHash {
			_ = s.RevokeSessionFamily(ctx, claims.SessionID)
			return "", ErrSessionBindingMismatch
		}
	}

	return s.GenerateToken(ctx, user)
}

// GenerateTokenPair issues an access token and a stored refresh token.
// A nonempty familyID continues a session family; an empty value starts a new one.
func (s *SessionService) GenerateTokenPair(ctx context.Context, user *User, familyID string) (*TokenPair, error) {
	if s.refreshTokenCache == nil {
		return nil, errors.New("refresh token cache not configured")
	}

	// Use the family ID as the access token sid so both credentials identify the same session
	// for family revocation and step-up grants.
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

// GenerateRefreshToken generates a random credential and stores its digest and metadata.
func (s *SessionService) GenerateRefreshToken(ctx context.Context, user *User, familyID string) (string, error) {
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
	ttl := time.Duration(s.options.RefreshTokenExpireDays) * 24 * time.Hour

	data := &RefreshTokenData{
		UserID:       user.ID,
		TokenVersion: ResolvedTokenVersion(user),
		FamilyID:     familyID,
		BindingHash:  SessionBindingHashFromContext(ctx),
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}

	// Persist token state before adding revocation indexes.
	if err := s.refreshTokenCache.StoreRefreshToken(ctx, tokenHash, data, ttl); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}

	// Index the token for user-wide revocation.
	if err := s.refreshTokenCache.AddToUserTokenSet(ctx, user.ID, tokenHash, ttl); err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to add token to user set: %v", err)
		// Index failure is logged; the stored token is still returned.
	}

	// Index the token for family-wide revocation.
	if err := s.refreshTokenCache.AddToFamilyTokenSet(ctx, familyID, tokenHash, ttl); err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to add token to family set: %v", err)
		// Index failure is logged; the stored token is still returned.
	}

	return rawToken, nil
}

// RefreshTokenPair validates and consumes a refresh token before issuing its successor.
// Once consumed, the old token stays invalid even if issuing the new pair fails.
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
			// Missing tokens may have expired or already been consumed.
			s.observer.Printf("service.auth", "[Auth] Refresh token not found, possible reuse attack")
			return nil, ErrRefreshTokenInvalid
		}
		s.observer.Printf("service.auth", "[Auth] Error getting refresh token: %v", err)
		return nil, ErrServiceUnavailable
	}

	// Check the stored expiration time as well as the cache TTL.
	if s.now().After(data.ExpiresAt) {
		_ = s.refreshTokenCache.DeleteRefreshToken(ctx, tokenHash)
		return nil, ErrRefreshTokenExpired
	}

	// Load current user state before consuming the credential.
	user, err := s.userRepo.GetByID(ctx, data.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// A missing user triggers family revocation.
			_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
			return nil, ErrRefreshTokenInvalid
		}
		s.observer.Printf("service.auth", "[Auth] Database error getting user for token refresh: %v", err)
		return nil, ErrServiceUnavailable
	}

	if !user.IsActive() {
		// Revoke the inactive user's session family.
		_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
		return nil, ErrUserNotActive
	}

	// Compare the current credential fingerprint to detect password or email changes.
	if data.TokenVersion != ResolvedTokenVersion(user) {
		// Revoke the family when its credential fingerprint is stale.
		_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
		return nil, ErrTokenRevoked
	}

	// When both bindings are present, a fingerprint mismatch triggers family revocation.
	// Unbound sessions may renew and acquire the current context fingerprint.
	if s.settingService != nil && s.settingService.IsSessionBindingEnabled(ctx) && data.BindingHash != "" {
		if current := SessionBindingHashFromContext(ctx); current != "" && current != data.BindingHash {
			_ = s.refreshTokenCache.DeleteTokenFamily(ctx, data.FamilyID)
			s.observer.Printf("service.auth", "[Auth] Session binding mismatch on refresh for user %d, family revoked", data.UserID)
			return nil, ErrSessionBindingMismatch
		}
	}

	// Claim consumption after validation so concurrent renewals or stale reads cannot issue another pair.
	consumed, err := s.refreshTokenCache.ConsumeRefreshToken(ctx, tokenHash)
	if err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to delete old refresh token: %v", err)
		return nil, ErrServiceUnavailable
	}
	if !consumed {
		return nil, ErrRefreshTokenInvalid
	}

	// Issue the successor in the same session family.
	pair, err := s.GenerateTokenPair(ctx, user, data.FamilyID)
	if err != nil {
		return nil, err
	}
	return &TokenPairWithUser{
		TokenPair: *pair,
		UserRole:  user.Role,
	}, nil
}

// RevokeRefreshToken deletes a single refresh credential.
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

// RevokeSessionFamily deletes refresh tokens indexed under one session family.
// Other families remain available, including sessions on other devices.
func (s *SessionService) RevokeSessionFamily(ctx context.Context, familyID string) error {
	if s.refreshTokenCache == nil || familyID == "" {
		return nil
	}
	return s.refreshTokenCache.DeleteTokenFamily(ctx, familyID)
}

// RevokeAllUserSessions deletes refresh tokens indexed for the user.
// Callers use this for password changes or logout across devices.
func (s *SessionService) RevokeAllUserSessions(ctx context.Context, userID int64) error {
	if s.refreshTokenCache == nil {
		return nil // No-op if cache not configured
	}
	return s.refreshTokenCache.DeleteUserRefreshTokens(ctx, userID)
}

// RevokeAllUserTokens verifies that the user exists and attempts to revoke all refresh sessions.
//
// Access-token versions are derived from email and password_hash; users has no token_version column.
// Changing those stored credentials invalidates access tokens through ResolvedTokenVersion.
// Updating the in-memory TokenVersion alone cannot invalidate persisted credentials.
// This method leaves access-token validity to those credential checks.
// Refresh-session cleanup failures are logged and do not change the returned result.
func (s *SessionService) RevokeAllUserTokens(ctx context.Context, userID int64) error {
	if _, err := s.userRepo.GetByID(ctx, userID); err != nil {
		return fmt.Errorf("get user: %w", err)
	}

	if err := s.RevokeAllUserSessions(ctx, userID); err != nil {
		s.observer.Printf("service.auth", "[Auth] Failed to revoke refresh sessions after token invalidation for user %d: %v", userID, err)
	}
	return nil
}

// HashToken returns the hex-encoded SHA-256 digest of a token.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func ResolvedTokenVersion(user *User) int64 {
	if user == nil {
		return 0
	}
	if user.TokenVersionResolved {
		return user.TokenVersion
	}

	material := strings.ToLower(strings.TrimSpace(user.Email)) + "\n" + user.PasswordHash
	sum := sha256.Sum256([]byte(material))
	fingerprint := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	return user.TokenVersion ^ fingerprint
}

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
