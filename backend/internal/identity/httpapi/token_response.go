package httpapi

import (
	"encoding/json"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// token_response.go issues login credentials and writes the shared HTTP token
// response after account validation.
// Successful login responses require a present, active account before
// credentials are issued. Token-pair failure triggers a separate access-token
// attempt, so clients can receive an access credential with omitted refresh
// and expiry fields when the second issuance succeeds.

// EnsureLoginUserActive rejects a missing or inactive account before login
// credentials are issued. Nil users receive INVALID_USER, while inactive
// accounts return the identity error used by the other login paths.
func EnsureLoginUserActive(user *identity.User) error {
	if user == nil {
		return infraerrors.Unauthorized("INVALID_USER", "user not found")
	}
	if !user.IsActive() {
		return identity.ErrUserNotActive
	}
	return nil
}

// RespondWithTokenPair writes credentials and account data for an active user.
// Pair issuance errors are logged and trigger an access-token-only attempt; if
// that also fails, the response reports an internal error.
func RespondWithTokenPair(c *gin.Context, authService *identity.AuthService, user *identity.User) {
	if err := EnsureLoginUserActive(user); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	tokenPair, err := authService.GenerateTokenPair(c.Request.Context(), user, "")
	if err != nil {
		slog.Error("failed to generate token pair", "error", err, "user_id", user.ID)
		// Pair issuance can fail before a refresh credential is available. A
		// separate access-token attempt lets the caller complete login when that
		// issuance path still succeeds, with refresh and expiry fields omitted.
		token, tokenErr := authService.GenerateToken(c.Request.Context(), user)
		if tokenErr != nil {
			response.InternalError(c, "Failed to generate token")
			return
		}
		response.Success(c, dto.AuthResponse[json.RawMessage]{
			AccessToken: token,
			TokenType:   "Bearer",
			User:        dto.UserFromIdentity[json.RawMessage](user, nil),
		})
		return
	}
	response.Success(c, dto.AuthResponse[json.RawMessage]{
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
		TokenType:    "Bearer",
		User:         dto.UserFromIdentity[json.RawMessage](user, nil),
	})
}
