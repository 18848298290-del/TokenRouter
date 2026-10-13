package httpapi_test

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Backend-mode guards either permit the configured administrator
// authentication surface or reject panel self-service. Tests install simple
// routes and runtime switches, checking user-role handling separately from the
// public auth-path allowlist and the nil-reader bypass.

// bmSettingRepo supplies the runtime string settings needed by backend-mode
// guard fixtures.
type bmSettingRepo struct {
	values map[string]string // configured runtime setting strings
}

// Get panics when a guard fixture reaches this unsupported settings repository
// operation. Tests supply direct value reads and bulk writes, so other access
// reveals an unexpected settings dependency.
func (r *bmSettingRepo) Get(_ context.Context, _ string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue returns the configured runtime string for the supplied fixture key.
// Missing keys return the settings sentinel so readers can apply their
// unconfigured backend-mode behavior.
func (r *bmSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	v, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return v, nil
}

// Set panics when a guard fixture reaches this unsupported settings repository
// operation. Tests supply direct value reads and bulk writes, so other access
// reveals an unexpected settings dependency.
func (r *bmSettingRepo) Set(_ context.Context, _, _ string) error {
	panic("unexpected Set call")
}

// GetMultiple panics when a guard fixture reaches this unsupported settings
// repository operation. Tests supply direct value reads and bulk writes, so
// other access reveals an unexpected settings dependency.
func (r *bmSettingRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

// SetMultiple copies supplied setting entries into the mutable fixture map. A
// nil map is allocated on demand, and keys absent from this write retain their
// existing values.
func (r *bmSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	if r.values == nil {
		r.values = make(map[string]string, len(settings))
	}
	maps.Copy(r.values, settings)
	return nil
}

// GetAll panics when a guard fixture reaches this unsupported settings
// repository operation. Tests supply direct value reads and bulk writes, so
// other access reveals an unexpected settings dependency.
func (r *bmSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics when a guard fixture reaches this unsupported settings
// repository operation. Tests supply direct value reads and bulk writes, so
// other access reveals an unexpected settings dependency.
func (r *bmSettingRepo) Delete(_ context.Context, _ string) error {
	panic("unexpected Delete call")
}

// newBackendModeSettingService builds a backend-mode reader and publishes the
// supplied enabled observation. Only exact true text publishes an enabled
// value; the fixture retains that raw string in its settings map.
func newBackendModeSettingService(t *testing.T, enabled string) *admission.BackendMode {
	t.Helper()

	repo := &bmSettingRepo{
		values: map[string]string{
			gateway.SettingKeyBackendModeEnabled: enabled,
		},
	}
	svc := admission.NewBackendMode(repo, nil)
	svc.Publish(enabled == "true")

	return svc
}

// stringPtr returns a pointer to the supplied fixture role text.
func stringPtr(v string) *string {
	return &v
}

// TestBackendModeUserGuard varies the runtime switch, reader presence, and
// supplied Gin role. It checks administrator admission plus rejection of
// ordinary, empty, or absent roles when the mode is enabled.
func TestBackendModeUserGuard(t *testing.T) {
	tests := []struct {
		name       string // guard fixture case
		nilService bool   // settings reader omitted flag
		enabled    string // raw backend-mode setting text
		// role supplies the Gin role when present. Nil leaves the compatibility
		// role absent so the guard exercises its missing-role response.
		role       *string
		wantStatus int // expected HTTP response status
	}{
		{
			name:       "disabled_allows_all",
			enabled:    "false",
			role:       stringPtr("user"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "nil_service_allows_all",
			nilService: true,
			role:       stringPtr("user"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_admin_allowed",
			enabled:    "true",
			role:       stringPtr("admin"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_user_blocked",
			enabled:    "true",
			role:       stringPtr("user"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_no_role_blocked",
			enabled:    "true",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_empty_role_blocked",
			enabled:    "true",
			role:       stringPtr(""),
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			if tc.role != nil {
				role := *tc.role
				r.Use(func(c *gin.Context) {
					c.Set(string(authctx.ContextKeyUserRole), role)
					c.Next()
				})
			}

			var svc *admission.BackendMode
			if !tc.nilService {
				svc = newBackendModeSettingService(t, tc.enabled)
			}

			var reader identityhttp.BackendModeReader
			if svc != nil {
				reader = svc
			}
			r.Use(identityhttp.BackendModeUserGuard(reader))
			r.GET("/test", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			r.ServeHTTP(w, req)

			require.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

// TestBackendModeAuthGuard varies runtime enablement across login, callback,
// pending, and self-service paths. It checks the allowlisted continuation
// statuses and rejects start, registration, and password-recovery routes under
// backend mode.
func TestBackendModeAuthGuard(t *testing.T) {
	tests := []struct {
		name       string // guard fixture case
		nilService bool   // settings reader omitted flag
		enabled    string // raw backend-mode setting text
		path       string // requested authentication route path
		wantStatus int    // expected HTTP response status
	}{
		{
			name:       "disabled_allows_all",
			enabled:    "false",
			path:       "/api/v1/auth/register",
			wantStatus: http.StatusOK,
		},
		{
			name:       "nil_service_allows_all",
			nilService: true,
			path:       "/api/v1/auth/register",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_login",
			enabled:    "true",
			path:       "/api/v1/auth/login",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_login_2fa",
			enabled:    "true",
			path:       "/api/v1/auth/login/2fa",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_passkey_login_begin",
			enabled:    "true",
			path:       "/api/v1/auth/passkey/login/begin",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_passkey_login_finish",
			enabled:    "true",
			path:       "/api/v1/auth/passkey/login/finish",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_logout",
			enabled:    "true",
			path:       "/api/v1/auth/logout",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_refresh",
			enabled:    "true",
			path:       "/api/v1/auth/refresh",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_linuxdo_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/linuxdo/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_linuxdo_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/linuxdo/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_wechat_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/wechat/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_wechat_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/wechat/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_wechat_payment_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/wechat/payment/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_wechat_payment_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/wechat/payment/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_oidc_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/oidc/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_oidc_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/oidc/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_github_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/github/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_github_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/github/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_github_complete_registration",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/github/complete-registration",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_google_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/google/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_google_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/google/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_google_one_tap",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/google/one-tap",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_google_complete_registration",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/google/complete-registration",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_dingtalk_oauth_start",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/dingtalk/start",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_allows_dingtalk_oauth_callback",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/dingtalk/callback",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_dingtalk_complete_registration",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/dingtalk/complete-registration",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_dingtalk_create_account",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/dingtalk/create-account",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_dingtalk_bind_login",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/dingtalk/bind-login",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_oauth_pending_exchange",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/pending/exchange",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_oauth_pending_send_verify_code",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/pending/send-verify-code",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_oauth_pending_create_account",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/pending/create-account",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_oauth_pending_bind_login",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/pending/bind-login",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_provider_bind_login",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/oidc/bind-login",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_provider_create_account",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/wechat/create-account",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_legacy_complete_registration",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/linuxdo/complete-registration",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_allows_email_oauth_complete_registration",
			enabled:    "true",
			path:       "/api/v1/auth/oauth/github/complete-registration",
			wantStatus: http.StatusOK,
		},
		{
			name:       "enabled_blocks_register",
			enabled:    "true",
			path:       "/api/v1/auth/register",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "enabled_blocks_forgot_password",
			enabled:    "true",
			path:       "/api/v1/auth/forgot-password",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()

			var svc *admission.BackendMode
			if !tc.nilService {
				svc = newBackendModeSettingService(t, tc.enabled)
			}

			var reader identityhttp.BackendModeReader
			if svc != nil {
				reader = svc
			}
			r.Use(identityhttp.BackendModeAuthGuard(reader))
			r.Any("/*path", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(w, req)

			require.Equal(t, tc.wantStatus, w.Code)
		})
	}
}
