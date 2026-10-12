package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Tests use production refresh and SDK parsing with fixed local transport responses.
type agRecoveryIdentityAdmin struct {
	providercore.ManagedCredentialStore
	current providercore.Record
	clears  int
}

type agRecoveryIdentityTransport struct{ admin *agRecoveryIdentityAdmin }

type grokRefreshOAuthStub struct {
	provider *providercore.Record
	info     *providercore.GrokTokenInfo
	calls    int
}

type grokRefreshAdminService struct {
	*managementMutationFixture
	updatedCredentials map[string]any
}

type applyOAuthTokenInvalidator struct {
	providers []*providercore.Record
}

func TestAntigravityManualRecoveryDoesNotClearNewAdministratorState(t *testing.T) {
	admin := &agRecoveryIdentityAdmin{current: providercore.Record{ID: 82, Platform: capability.PlatformAntigravity, Type: capability.ProviderTypeOAuth, Status: providercore.StatusError, ErrorMessage: "missing_project_id: original", Credentials: map[string]any{"access_token": "old", "refresh_token": "old"}}}
	observed := admin.current
	transport := http.DefaultTransport
	http.DefaultTransport = agRecoveryIdentityTransport{admin}
	t.Cleanup(func() { http.DefaultTransport = transport })
	h := newManagedRefreshFixture(admin, &providercore.ManualCredentialExchange{Antigravity: providercore.NewAntigravityAuthorization(provideradapter.AntigravityAuthorizationOptions(nil))})
	_, _, err := h.Refresh(context.Background(), &observed)
	require.NoError(t, err)
	require.Equal(t, billing.StatusDisabled, admin.current.Status)
	require.Zero(t, admin.clears)
}

func TestRefreshSingleProviderRoutesGrokThroughGrokOAuthService(t *testing.T) {
	t.Parallel()

	adminSvc := &grokRefreshAdminService{managementMutationFixture: newManagementMutationFixture()}
	grokOAuth := &grokRefreshOAuthStub{info: &providercore.GrokTokenInfo{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    1_800_000_000,
	}}
	handler := newManagedRefreshFixture(adminSvc, &providercore.ManualCredentialExchange{Grok: grokOAuth})
	provider := &providercore.Record{
		ID:       4227,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "old-access",
			"refresh_token":      "old-refresh",
			"base_url":           "https://example.invalid/v1",
			"subscription_tier":  "SUPER_GROK",
			"entitlement_status": "ACTIVE",
		},
	}

	updated, warning, err := handler.Refresh(context.Background(), provider)
	require.NoError(t, err)
	require.Empty(t, warning)
	require.Equal(t, 1, grokOAuth.calls)
	// The exchange receives the complete provider record.
	require.Equal(t, provider, grokOAuth.provider)
	require.Equal(t, "new-access", adminSvc.updatedCredentials["access_token"])
	require.Equal(t, "new-refresh", adminSvc.updatedCredentials["refresh_token"])
	require.Equal(t, "https://example.invalid/v1", adminSvc.updatedCredentials["base_url"])
	require.Equal(t, "SUPER_GROK", adminSvc.updatedCredentials["subscription_tier"])
	require.Equal(t, "ACTIVE", adminSvc.updatedCredentials["entitlement_status"])
	require.Equal(t, adminSvc.updatedCredentials, updated.Credentials)
}

// TestRefreshSingleProvider_RejectsShadow checks that manual refresh rejects Spark shadows before contacting upstream.
// Shadows use their parent's credentials; single and batch refresh share this check.
func TestRefreshSingleProvider_RejectsShadow(t *testing.T) {
	h := providercore.NewManagedRefreshService(providercore.ManagedRefreshOptions{}) // Shadow validation returns before using dependencies.
	parentID := int64(5)
	shadow := &providercore.Record{
		ID:               9,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth, // OAuth eligibility passes so the shadow check is exercised.
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}

	_, _, err := h.Refresh(context.Background(), shadow)
	require.Error(t, err, "影子刷新应被早拒")
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
}

func TestApplyOAuthCredentialsDiscardsDeprecatedLongContextBillingExtra(t *testing.T) {
	stub := newManagementMutationFixture()
	stub.providers = []providercore.Record{{
		ID:       1,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
	}}
	handler := newMutationHandler(stub, nil)
	router := gin.New()
	router.POST("/providers/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/providers/1/apply-oauth-credentials", bytes.NewBufferString(
		`{"type":"oauth","credentials":{"access_token":"new-token"},"extra":{"openai_long_context_billing_enabled":1,"preserved":"value"}}`,
	))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotNil(t, stub.updateProviderInput)
	require.Len(t, stub.updateExtraCalls, 1)
	require.NotContains(t, stub.updateExtraCalls[0], deprecatedLongContextBillingExtraKey)
	require.Equal(t, "value", stub.updateExtraCalls[0]["preserved"])
}

func TestProviderHandlerApplyOAuthCredentials_MergesExtraAndInvalidatesToken(t *testing.T) {
	adminSvc := newManagementMutationFixture()
	invalidator := &applyOAuthTokenInvalidator{}
	handler := newMutationHandler(adminSvc, invalidator)
	router := gin.New()
	router.POST("/api/v1/admin/providers/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

	payload := map[string]any{
		"type": "oauth",
		"credentials": map[string]any{
			"access_token": "new-access-token",
			"expires_at":   "1893456000",
		},
		"extra": map[string]any{
			"account_uuid": "new-provider-uuid",
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/3/apply-oauth-credentials", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, adminSvc.updateProviderInput)
	require.Equal(t, capability.ProviderTypeOAuth, adminSvc.updateProviderInput.Type)
	require.Equal(t, "new-access-token", adminSvc.updateProviderInput.Credentials["access_token"])
	require.Nil(t, adminSvc.updateProviderInput.Extra, "凭据更新不应全量覆盖 Extra")
	require.Len(t, adminSvc.updateExtraCalls, 1)
	require.Equal(t, "new-provider-uuid", adminSvc.updateExtraCalls[0]["account_uuid"])
	require.Equal(t, []int64{int64(3)}, adminSvc.clearProviderErrorIDs)
	require.Len(t, invalidator.providers, 1)
	require.Equal(t, int64(3), invalidator.providers[0].ID)
}

// TestManualRefreshDoesNotOverwriteNewAdministratorCredentials checks that reauthorization during exchange discards a late refresh result.
func TestManualRefreshDoesNotOverwriteNewAdministratorCredentials(t *testing.T) {
	adminSvc := newManagementMutationFixture()
	adminSvc.providers = []providercore.Record{{ID: 977, Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Status: billing.StatusActive, Credentials: map[string]any{"refresh_token": "observed", "security_oauth_token": "old", "machine_id": "machine"}}}
	observed := adminSvc.providers[0]
	exchange := &providercore.ManualCredentialExchange{Qoder: func(context.Context, *providercore.Record) (map[string]any, error) {
		adminSvc.providers[0].Credentials = map[string]any{"refresh_token": "administrator", "security_oauth_token": "administrator-token", "machine_id": "machine"}
		return map[string]any{"refresh_token": "late", "security_oauth_token": "late-token", "machine_id": "machine"}, nil
	}}
	h := newManagedRefreshFixture(adminSvc, exchange)
	updated, _, err := h.Refresh(context.Background(), &observed)
	require.NoError(t, err)
	require.Nil(t, adminSvc.updateProviderInput, "迟到交换不应提交旧凭据")
	require.Equal(t, "administrator", updated.GetCredential("refresh_token"))
}

func (s *agRecoveryIdentityAdmin) GetProvider(context.Context, int64) (*providercore.Record, error) {
	v := s.current
	return &v, nil
}

func (s *agRecoveryIdentityAdmin) UpdateProvider(_ context.Context, _ int64, input *providercore.UpdateProviderInput) (*providercore.Record, error) {
	s.current.Credentials = input.Credentials
	return &s.current, nil
}

func (s *agRecoveryIdentityAdmin) ClearProviderError(context.Context, int64) (*providercore.Record, error) {
	s.clears++
	s.current.Status = billing.StatusActive
	s.current.ErrorMessage = ""
	return &s.current, nil
}

func (s *agRecoveryIdentityAdmin) EnsureAntigravityPrivacy(context.Context, *providercore.Record) string {
	return "privacy_set"
}

func (s *agRecoveryIdentityAdmin) EnsureOpenAIPrivacy(context.Context, *providercore.Record) string {
	return ""
}

func (t agRecoveryIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{}`
	if strings.Contains(r.URL.Path, "token") {
		body = `{"access_token":"refreshed","token_type":"Bearer","expires_in":3600}`
	} else if strings.Contains(r.URL.Path, "loadCodeAssist") {
		t.admin.current.Status = billing.StatusDisabled
		t.admin.current.Credentials = map[string]any{"access_token": "administrator", "refresh_token": "administrator", "project_id": "new-project"}
		body = `{"cloudaicompanionProject":"recovered-project","currentTier":{"id":"STANDARD"}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// ClearManagedRefreshError models the conditional update; integration tests cover SQL interleaving.
func (s *agRecoveryIdentityAdmin) ClearManagedRefreshError(_ context.Context, old *providercore.Record) (*providercore.Record, bool, error) {
	if !providercore.ObserveManagedRecovery(old).Matches(&s.current) {
		return &s.current, false, nil
	}
	s.clears++
	s.current.Status = billing.StatusActive
	s.current.ErrorMessage = ""
	return &s.current, true, nil
}

func (s *grokRefreshOAuthStub) RefreshProviderToken(_ context.Context, provider *providercore.Record) (*providercore.GrokTokenInfo, error) {
	s.calls++
	s.provider = provider
	return s.info, nil
}

func (s *grokRefreshOAuthStub) BuildProviderCredentials(info *providercore.GrokTokenInfo) map[string]any {
	return map[string]any{
		"access_token":  info.AccessToken,
		"refresh_token": info.RefreshToken,
		"expires_at":    info.ExpiresAt,
		"base_url":      "https://api.x.ai/v1",
	}
}

func (s *grokRefreshAdminService) UpdateProvider(_ context.Context, id int64, input *providercore.UpdateProviderInput) (*providercore.Record, error) {
	s.updatedCredentials = input.Credentials
	return &providercore.Record{
		ID:          id,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: input.Credentials,
	}, nil
}

func (i *applyOAuthTokenInvalidator) InvalidateToken(ctx context.Context, provider *providercore.Record) error {
	i.providers = append(i.providers, provider)
	return nil
}
