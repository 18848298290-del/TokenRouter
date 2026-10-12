package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// stubJWTUserRepo implements GetByID for middleware tests.
type stubJWTUserRepo struct {
	identity.
		UserRepository
	users map[int64]*identity.User
}

type recordingActivityToucher struct {
	userIDs []int64
}

func (r *stubJWTUserRepo) GetByID(_ context.Context, id int64) (*identity.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return u, nil
}

func (r *stubJWTUserRepo) GetUserAvatar(_ context.Context, _ int64) (*identity.UserAvatar, error) {
	return nil, nil
}

func (r *stubJWTUserRepo) UpdateUserLastActiveAt(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

func (r *recordingActivityToucher) TouchLastActiveForUser(_ context.Context, user *identity.User) {
	if user == nil {
		return
	}
	r.userIDs = append(r.userIDs, user.ID)
}

// newJWTTestEnv returns a router with JWT authentication and a session service
// that tests use to issue tokens.
func newJWTTestEnv(users map[int64]*identity.User) (*gin.Engine, *identity.SessionService) {
	cfg := &config.Config{}
	cfg.JWT.Secret = "test-jwt-secret-32bytes-long!!!"
	cfg.JWT.AccessTokenExpireMinutes = 60

	userRepo := &stubJWTUserRepo{users: users}
	authSvc := identity.NewSessionService(identity.SessionOptions{Secret: cfg.JWT.Secret, ExpireHour: cfg.JWT.ExpireHour, AccessTokenExpireMinutes: cfg.JWT.AccessTokenExpireMinutes, RefreshTokenExpireDays: cfg.JWT.RefreshTokenExpireDays}, userRepo, nil, nil, nil)
	userSvc := identity.NewUserService(userRepo, nil, nil, nil, func(_ string, fn func()) bool { go fn(); return true })
	mw := identityhttp.JWTAuth(authSvc, userSvc, userSvc, nil, nil)

	r := gin.New()
	r.Use(mw)
	r.GET("/protected", func(c *gin.Context) {
		subject, _ := authctx.GetAuthSubjectFromContext(c)
		role, _ := authctx.GetUserRoleFromContext(c)
		c.JSON(http.StatusOK, gin.H{
			"user_id": subject.UserID,
			"role":    role,
		})
	})
	return r, authSvc
}

func TestJWTAuth_ValidToken(t *testing.T) {
	user := &identity.User{
		ID:           1,
		Email:        "test@example.com",
		Role:         "user",
		Status:       billing.StatusActive,
		Concurrency:  5,
		TokenVersion: 1,
	}
	router, authSvc := newJWTTestEnv(map[int64]*identity.User{1: user})

	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, float64(1), body["user_id"])
	require.Equal(t, "user", body["role"])
}

func TestJWTAuth_ValidToken_LowercaseBearer(t *testing.T) {
	user := &identity.User{
		ID:           1,
		Email:        "test@example.com",
		Role:         "user",
		Status:       billing.StatusActive,
		Concurrency:  5,
		TokenVersion: 1,
	}
	router, authSvc := newJWTTestEnv(map[int64]*identity.User{1: user})

	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestJWTAuth_ValidToken_TouchesLastActive(t *testing.T) {
	user := &identity.User{
		ID:           1,
		Email:        "test@example.com",
		Role:         "user",
		Status:       billing.StatusActive,
		Concurrency:  5,
		TokenVersion: 1,
	}

	cfg := &config.Config{}
	cfg.JWT.Secret = "test-jwt-secret-32bytes-long!!!"
	cfg.JWT.AccessTokenExpireMinutes = 60

	userRepo := &stubJWTUserRepo{users: map[int64]*identity.User{1: user}}
	authSvc := identity.NewSessionService(identity.SessionOptions{Secret: cfg.JWT.Secret, ExpireHour: cfg.JWT.ExpireHour, AccessTokenExpireMinutes: cfg.JWT.AccessTokenExpireMinutes, RefreshTokenExpireDays: cfg.JWT.RefreshTokenExpireDays}, userRepo, nil, nil, nil)
	userSvc := identity.NewUserService(userRepo, nil, nil, nil, func(_ string, fn func()) bool { go fn(); return true })
	toucher := &recordingActivityToucher{}

	r := gin.New()
	r.Use(identityhttp.JWTAuth(authSvc, userSvc, toucher, nil, nil))
	r.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []int64{1}, toucher.userIDs)
}

func TestJWTAuth_MissingAuthorizationHeader(t *testing.T) {
	router, _ := newJWTTestEnv(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body httpx.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "UNAUTHORIZED", body.Code)
}

func TestJWTAuth_InvalidHeaderFormat(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"无Bearer前缀", "Token abc123"},
		{"缺少空格分隔", "Bearerabc123"},
		{"仅有单词", "abc123"},
	}
	router, _ := newJWTTestEnv(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", tt.header)
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			var body httpx.ErrorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "INVALID_AUTH_HEADER", body.Code)
		})
	}
}

func TestJWTAuth_EmptyToken(t *testing.T) {
	router, _ := newJWTTestEnv(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer ")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body httpx.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "EMPTY_TOKEN", body.Code)
}

func TestJWTAuth_TamperedToken(t *testing.T) {
	router, _ := newJWTTestEnv(nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjoxfQ.invalid_signature")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body httpx.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "INVALID_TOKEN", body.Code)
}

func TestJWTAuth_UserNotFound(t *testing.T) {
	// Issue a token for a user absent from the repository.
	fakeUser := &identity.User{
		ID:           999,
		Email:        "ghost@example.com",
		Role:         "user",
		Status:       billing.StatusActive,
		TokenVersion: 1,
	}

	router, authSvc := newJWTTestEnv(map[int64]*identity.User{})

	token, err := authSvc.GenerateToken(context.Background(), fakeUser)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body httpx.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "USER_NOT_FOUND", body.Code)
}

func TestJWTAuth_UserInactive(t *testing.T) {
	user := &identity.User{
		ID:           1,
		Email:        "disabled@example.com",
		Role:         "user",
		Status:       billing.StatusDisabled,
		TokenVersion: 1,
	}
	router, authSvc := newJWTTestEnv(map[int64]*identity.User{1: user})

	token, err := authSvc.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body httpx.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "USER_INACTIVE", body.Code)
}

func TestJWTAuth_TokenVersionMismatch(t *testing.T) {
	// The issued token has version 1 while current user state has version 2.
	userForToken := &identity.User{
		ID:           1,
		Email:        "test@example.com",
		Role:         "user",
		Status:       billing.StatusActive,
		TokenVersion: 1,
	}
	userInDB := &identity.User{
		ID:           1,
		Email:        "test@example.com",
		Role:         "user",
		Status:       billing.StatusActive,
		TokenVersion: 2, // Changed password version.
	}
	router, authSvc := newJWTTestEnv(map[int64]*identity.User{1: userInDB})

	token, err := authSvc.GenerateToken(context.Background(), userForToken)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body httpx.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "TOKEN_REVOKED", body.Code)
}
