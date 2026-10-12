package httpapi

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

// This fixture replaces credentials and non-tier configuration during the Drive query.
type tierIdentityPort interface {
	ProviderManagement
	provider.TierManagementStore
}

type tierIdentityAdmin struct {
	tierIdentityPort
	current provider.Record
}

type tierIdentityDrive struct{ admin *tierIdentityAdmin }

func TestTierRefreshDoesNotOverwriteNewCredentials(t *testing.T) {
	admin := &tierIdentityAdmin{current: provider.Record{ID: 984, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Credentials: map[string]any{"oauth_type": "google_one", "access_token": "old", "tier_id": "old"}, Extra: map[string]any{"admin_setting": "old"}}}
	gemini := provider.NewGeminiAuthorization(nil, nil, tierIdentityDrive{admin}, provideradapter.GeminiAuthorizationOptions(func() geminicli.OAuthConfig { return geminicli.OAuthConfig{} }, nil))
	h := NewManagementHandler(admin, ManagementOptions{Tier: provider.NewTierManagement(admin, provider.ProviderTierManagementOptions(gemini))})
	router := gin.New()
	router.POST("/:id/tier", h.RefreshTier)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/984/tier", nil))
	require.Equal(t, "admin-new", admin.current.Credentials["access_token"])
	require.Equal(t, "new", admin.current.Extra["admin_setting"])
}

func (s *tierIdentityAdmin) GetProvider(context.Context, int64) (*provider.Record, error) {
	v := s.current
	v.Credentials = map[string]any{}
	maps.Copy(v.Credentials, s.current.Credentials)
	v.Extra = map[string]any{}
	maps.Copy(v.Extra, s.current.Extra)
	return &v, nil
}

func (s *tierIdentityAdmin) UpdateProvider(_ context.Context, _ int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	if input.ExpectedCredentials != nil && !provider.MatchesCredentialVersion(&s.current, *input.ExpectedCredentials) {
		return nil, provider.ErrRefreshProviderStateChanged
	}
	if input.Credentials != nil {
		s.current.Credentials = input.Credentials
	}
	if input.Extra != nil {
		s.current.Extra = input.Extra
	}
	return &s.current, nil
}

func (d tierIdentityDrive) GetStorageQuota(context.Context, string, string) (*geminicli.DriveStorageInfo, error) {
	d.admin.current.Credentials = map[string]any{"oauth_type": "google_one", "access_token": "admin-new", "tier_id": "current"}
	d.admin.current.Extra = map[string]any{"admin_setting": "new"}
	return &geminicli.DriveStorageInfo{Limit: 2 * 1024 * 1024 * 1024 * 1024, Usage: 1}, nil
}
