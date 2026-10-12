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

// Tier storage needs the identity observed before a Drive query to remain
// current when its result is applied. management_tier.go is exercised with an
// inline administrator credential replacement, protecting those newer
// credentials and unrelated configuration from the late observation.

// tierIdentityPort combines HTTP administration and tier configuration
// operations for the identity fixture.
type tierIdentityPort interface {
	ProviderManagement           // embedded HTTP administration operations
	provider.TierManagementStore // embedded tier storage operations
}

// tierIdentityAdmin keeps serial provider state for a Drive-query
// identity-change interleaving.
type tierIdentityAdmin struct {
	tierIdentityPort                 // unused embedded fixture operations
	current          provider.Record // serial mutable provider fixture
}

// tierIdentityDrive changes provider identity during the fixed Drive storage
// observation.
type tierIdentityDrive struct {
	admin *tierIdentityAdmin // borrowed serial administrator state
}

// TestTierRefreshDoesNotOverwriteNewCredentials changes credentials and
// unrelated Extra during a controlled Drive query. The tier refresh preserves
// those newer administrator values when its observed identity no longer matches
// the stored fixture.
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

// GetProvider returns a record copy with new top-level credential and Extra
// maps. Nested values remain shared, and this serial fixture omits
// synchronization because the injected Drive callback mutates state inline.
func (s *tierIdentityAdmin) GetProvider(context.Context, int64) (*provider.Record, error) {
	v := s.current
	v.Credentials = map[string]any{}
	maps.Copy(v.Credentials, s.current.Credentials)
	v.Extra = map[string]any{}
	maps.Copy(v.Extra, s.current.Extra)
	return &v, nil
}

// UpdateProvider compares any expected credential version before replacing
// supplied fixture maps. Conflict returns the refresh-state-changed error;
// accepted replacements and returned record pointers remain borrowed by this
// serial test.
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

// GetStorageQuota replaces the administrator token and unrelated Extra before
// returning a fixed storage observation. This injected interleaving makes the
// later conditional tier save see a different identity.
func (d tierIdentityDrive) GetStorageQuota(context.Context, string, string) (*geminicli.DriveStorageInfo, error) {
	d.admin.current.Credentials = map[string]any{"oauth_type": "google_one", "access_token": "admin-new", "tier_id": "current"}
	d.admin.current.Extra = map[string]any{"admin_setting": "new"}
	return &geminicli.DriveStorageInfo{Limit: 2 * 1024 * 1024 * 1024 * 1024, Usage: 1}, nil
}
