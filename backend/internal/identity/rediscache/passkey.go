package rediscache

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// passkey.go stores and consumes temporary WebAuthn ceremony state in Redis.
// Passkey begin operations save WebAuthn ceremony state behind an opaque random
// token, and finish operations consume that state once. The Redis record is
// separate from the durable Passkey credential. Finish removes saved state before
// SDK verification, so a verification failure leaves its challenge consumed.
//
// Store returns a token after the session JSON has been written with a positive
// TTL. Consume trims and bounds that token, then uses GETDEL before decoding the
// record. Missing or corrupt state is a session error; transport failures remain
// storage errors. The identity service checks the recovered ceremony and performs
// SDK verification after this irreversible consumption step.

// passkeySessionPrefix separates short-lived WebAuthn session records from other
// identity credentials and uses the returned opaque token as its suffix.
const passkeySessionPrefix = "passkey:session:"

// passkeySessionStore persists and consumes identity ceremony sessions through Redis commands.
type passkeySessionStore struct {
	// redis is the shared client retained for session operations. Its caller
	// owns connection cleanup after all Passkey begin and finish calls complete.
	redis *redis.Client
}

// NewPasskeySessionStore binds one-time ceremony storage to the supplied Redis
// client. The store retains that client; its caller owns connection cleanup after
// Passkey services finish their session operations.
func NewPasskeySessionStore(redisClient *redis.Client) identity.PasskeySessionStore {
	return &passkeySessionStore{redis: redisClient}
}

// Store serializes a nonnil ceremony session under a token containing 32 random
// bytes encoded as unpadded base64url. TTL must be positive, and the token is
// returned after Redis accepts the saved session.
func (s *passkeySessionStore) Store(
	ctx context.Context,
	session *identity.PasskeySession,
	ttl time.Duration,
) (string, error) {
	if session == nil || ttl <= 0 {
		return "", fmt.Errorf("invalid passkey session")
	}
	// Encode 256 random bits as the session lookup credential.
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate passkey session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	payload, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("encode passkey session: %w", err)
	}
	if err = s.redis.Set(ctx, passkeySessionPrefix+token, payload, ttl).Err(); err != nil {
		return "", fmt.Errorf("store passkey session: %w", err)
	}
	return token, nil
}

// Consume removes and decodes the ceremony state selected by a trimmed token.
// Empty tokens and tokens longer than 128 bytes return ErrPasskeySession, as do
// missing keys and undecodable saved payloads. Redis failures remain wrapped
// storage errors for the authentication caller.
//
// GETDEL supplies the removal and read in one Redis operation, so one successful
// consumer receives the payload. Decoding follows removal: a malformed payload
// remains consumed even when decoding fails. The core checks ceremony kind before
// WebAuthn verification, and registration also checks the recovered user identifier.
func (s *passkeySessionStore) Consume(
	ctx context.Context,
	token string,
) (*identity.PasskeySession, error) {
	token = strings.TrimSpace(token)
	// Bound the credential before constructing its Redis lookup key.
	if token == "" || len(token) > 128 {
		return nil, identity.ErrPasskeySession
	}
	// Remove while reading so concurrent finish attempts cannot obtain the same
	// saved challenge; decoding errors after this point also leave that session consumed.
	payload, err := s.redis.GetDel(ctx, passkeySessionPrefix+token).Bytes()
	if err == redis.Nil {
		return nil, identity.ErrPasskeySession
	}
	if err != nil {
		return nil, fmt.Errorf("consume passkey session: %w", err)
	}
	var session identity.PasskeySession
	if err = json.Unmarshal(payload, &session); err != nil {
		// Corrupt saved state cannot provide a ceremony for verification, so expose
		// the same session rejection as an absent challenge after GETDEL consumed it.
		return nil, identity.ErrPasskeySession
	}
	return &session, nil
}
