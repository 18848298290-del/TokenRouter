package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type codexImportMemoryAdminService struct {
	*managementMutationFixture
	nextID           int64
	updatedProviders []struct {
		id    int64
		input *provider.UpdateProviderInput
	}
}

func TestNormalizeCodexImportEntryAcceptsAgentIdentityAuthJSON(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	privateKeyBase64 := base64.StdEncoding.EncodeToString(der)

	item, err := provider.NormalizeCodexImportEntry(provider.CodexImportEntry{
		Index: 1,
		Value: map[string]any{
			"auth_mode": "agentIdentity",
			"agent_identity": map[string]any{
				"agent_runtime_id":           "runtime-import",
				"agent_private_key":          privateKeyBase64,
				"account_id":                 "provider-import",
				"chatgpt_user_id":            "user-import",
				"email":                      "agent@example.invalid",
				"plan_type":                  "pro",
				"chatgpt_account_is_fedramp": false,
			},
		},
	}, codexImportFixtureOptions())
	require.NoError(t, err)
	require.NotNil(t, item)
	require.True(t, item.IsAgentIdentity)
	require.Equal(t, provider.OpenAIAuthModeAgentIdentity, item.Credentials["auth_mode"])
	require.Equal(t, "runtime-import", item.Credentials["agent_runtime_id"])
	require.Equal(t, privateKeyBase64, item.Credentials["agent_private_key"])
	require.Equal(t, "provider-import", item.Credentials["chatgpt_account_id"])
	require.Equal(t, "user-import", item.Credentials["chatgpt_user_id"])
	require.NotContains(t, item.Credentials, "access_token")
	require.NotContains(t, item.Credentials, "refresh_token")
	require.NotEmpty(t, item.WarningTexts)
}

func TestImportCodexSessionsKeepsAgentIdentityTeamsSeparate(t *testing.T) {
	first := buildAgentIdentityImportValue(t, "runtime-a", "team-a", "same-user", "task-a")
	second := buildAgentIdentityImportValue(t, "runtime-b", "team-b", "same-user", "task-b")
	svc := newCodexImportMemoryAdminService(nil)
	handler := newCodexImportFixture(svc)

	result, err := handler.Import(context.Background(), provider.CodexSessionImportRequest{}, []provider.CodexImportEntry{{Index: 1, Value: first}, {Index: 2, Value: second}})
	require.NoError(t, err)
	require.Equal(t, 2, result.Created)
	require.Zero(t, result.Updated)
	require.Zero(t, result.Skipped)
	require.Len(t, svc.createdProviders, 2)
}

func TestImportCodexSessionsMergesAgentIdentityRuntimesForSameTeam(t *testing.T) {
	first := buildAgentIdentityImportValue(t, "runtime-a", "team-a", "same-user", "task-a")
	second := buildAgentIdentityImportValue(t, "runtime-b", "team-a", "same-user", "task-b")
	firstIdentity, ok := first["agent_identity"].(map[string]any)
	require.True(t, ok)
	existing := provider.Record{
		ID:       41,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          provider.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   firstIdentity["agent_runtime_id"],
			"agent_private_key":  firstIdentity["agent_private_key"],
			"task_id":            firstIdentity["task_id"],
			"chatgpt_account_id": firstIdentity["account_id"],
			"chatgpt_user_id":    firstIdentity["chatgpt_user_id"],
		},
	}
	svc := newCodexImportMemoryAdminService([]provider.Record{existing})
	handler := newCodexImportFixture(svc)

	result, err := handler.Import(context.Background(), provider.CodexSessionImportRequest{}, []provider.CodexImportEntry{{Index: 1, Value: second}})
	require.NoError(t, err)
	require.Zero(t, result.Created)
	require.Equal(t, 1, result.Updated)
	require.Len(t, svc.updatedProviders, 1)
	require.Equal(t, "runtime-b", svc.updatedProviders[0].input.Credentials["agent_runtime_id"])
	require.Equal(t, "task-b", svc.updatedProviders[0].input.Credentials["task_id"])
}

func TestImportCodexSessionsCreatesAgentIdentityWithoutOAuthExpiry(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)

	svc := newCodexImportMemoryAdminService(nil)
	handler := newCodexImportFixture(svc)
	result, err := handler.Import(context.Background(), provider.CodexSessionImportRequest{}, []provider.CodexImportEntry{{
		Index: 1,
		Value: map[string]any{
			"auth_mode": "agentIdentity",
			"agent_identity": map[string]any{
				"agent_runtime_id":  "runtime-import",
				"agent_private_key": base64.StdEncoding.EncodeToString(der),
				"task_id":           "task-import",
				"account_id":        "provider-import",
				"chatgpt_user_id":   "user-import",
			},
		},
	}})
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Zero(t, result.Failed)
	require.Len(t, svc.createdProviders, 1)
	created := svc.createdProviders[0]
	require.Nil(t, created.ExpiresAt)
	require.Nil(t, created.AutoPauseOnExpired)
	require.Equal(t, provider.OpenAIAuthModeAgentIdentity, created.Credentials["auth_mode"])
	require.NotContains(t, created.Credentials, "access_token")
	require.NotContains(t, created.Credentials, "refresh_token")
}

func TestCodexSessionImportDiscardsDeprecatedLongContextBillingExtra(t *testing.T) {
	stub := newCodexImportMemoryAdminService(nil)
	handler := NewCodexImportHandler(newCodexImportFixture(stub))
	router := gin.New()
	router.POST("/providers/import-codex-session", handler.ImportCodexSession)
	body, err := json.Marshal(provider.CodexSessionImportRequest{
		Content: buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour)),
		Extra:   map[string]any{deprecatedLongContextBillingExtraKey: []bool{true}, "preserved": "value"},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/providers/import-codex-session", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Len(t, stub.createdProviders, 1)
	require.NotContains(t, stub.createdProviders[0].Extra, deprecatedLongContextBillingExtraKey)
	require.Equal(t, "value", stub.createdProviders[0].Extra["preserved"])
}

func TestImportCodexSessionsAccessTokenOnlySameWorkspaceDifferentUsersCreatesTwoProviders(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "workspace-1", "user-1")},
		{Index: 2, Value: buildCodexAccessOnlyImportValue(t, "workspace-1", "user-2")},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Created != 2 || result.Updated != 0 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want two created providers", result)
	}
	if len(svc.createdProviders) != 2 {
		t.Fatalf("created providers = %d, want 2", len(svc.createdProviders))
	}
	if svc.createdProviders[0].Credentials["chatgpt_user_id"] == svc.createdProviders[1].Credentials["chatgpt_user_id"] {
		t.Fatalf("created providers share user id: %v", svc.createdProviders)
	}
}

func TestImportCodexSessionsAccessTokenOnlySameWorkspaceAndUserDifferentTokensCreatesTwoProviders(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: map[string]any{
			"access_token": buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{
				"sub": "shared-user",
				"jti": "token-1",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "workspace-1",
				},
			}),
		}},
		{Index: 2, Value: map[string]any{
			"access_token": buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{
				"sub": "shared-user",
				"jti": "token-2",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "workspace-1",
				},
			}),
		}},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Created != 2 || result.Updated != 0 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want two created providers", result)
	}
	if len(svc.createdProviders) != 2 {
		t.Fatalf("created providers = %d, want 2", len(svc.createdProviders))
	}
}

func TestImportCodexSessionsAccessTokenOnlySameUserUpdatesExisting(t *testing.T) {
	existingToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	svc := newCodexImportMemoryAdminService([]provider.Record{{
		ID:       10,
		Name:     "existing",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       existingToken,
		},
	}})
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: map[string]any{"access_token": existingToken}},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Created != 0 || result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want one updated provider", result)
	}
	if len(svc.createdProviders) != 0 {
		t.Fatalf("created providers = %d, want 0", len(svc.createdProviders))
	}
	if len(svc.updatedProviders) != 1 || svc.updatedProviders[0].id != 10 {
		t.Fatalf("updated providers = %+v, want provider 10", svc.updatedProviders)
	}
}

func TestImportCodexSessionsUpgradesAccessTokenOnlyProviderWithRefreshToken(t *testing.T) {
	oldToken := buildCodexAccessTokenWithJTI(t, "workspace-1", "user-1", "old-token", time.Now().Add(time.Hour))
	newToken := buildCodexAccessTokenWithJTI(t, "workspace-1", "user-1", "new-token", time.Now().Add(time.Hour))
	svc := newCodexImportMemoryAdminService([]provider.Record{{
		ID:       12,
		Name:     "existing",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       oldToken,
		},
	}})
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: map[string]any{
			"access_token":  newToken,
			"refresh_token": "refresh-new",
		}},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Created != 0 || result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want one updated provider", result)
	}
	if len(svc.updatedProviders) != 1 || svc.updatedProviders[0].id != 12 {
		t.Fatalf("updated providers = %+v, want provider 12", svc.updatedProviders)
	}
	if got := svc.updatedProviders[0].input.Credentials["refresh_token"]; got != "refresh-new" {
		t.Fatalf("updated refresh_token = %v, want refresh-new", got)
	}
}

func TestImportCodexSessionsAccessTokenOnlyPreservesExistingRefreshToken(t *testing.T) {
	existingToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	svc := newCodexImportMemoryAdminService([]provider.Record{{
		ID:       13,
		Name:     "existing",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       existingToken,
			"refresh_token":      "refresh-old",
			"client_id":          "client-old",
		},
	}})
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: map[string]any{"access_token": existingToken}},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Created != 0 || result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want one updated provider", result)
	}
	update := svc.updatedProviders[0].input
	if got := update.Credentials["refresh_token"]; got != "refresh-old" {
		t.Fatalf("refresh_token = %v, want refresh-old", got)
	}
	if got := update.Credentials["client_id"]; got != "client-old" {
		t.Fatalf("client_id = %v, want client-old", got)
	}
	if update.ExpiresAt != nil {
		t.Fatalf("ExpiresAt = %v, want nil to preserve OAuth provider expiry", *update.ExpiresAt)
	}
	if update.AutoPauseOnExpired != nil {
		t.Fatalf("AutoPauseOnExpired = %v, want nil to preserve OAuth provider scheduling", *update.AutoPauseOnExpired)
	}
}

func TestImportCodexSessionsBatchOldAccessTokenDoesNotRollbackRefreshToken(t *testing.T) {
	oldToken := buildCodexAccessTokenWithJTI(t, "workspace-1", "user-1", "old-token", time.Now().Add(time.Hour))
	newToken := buildCodexAccessTokenWithJTI(t, "workspace-1", "user-1", "new-token", time.Now().Add(time.Hour))
	svc := newCodexImportMemoryAdminService([]provider.Record{{
		ID:       14,
		Name:     "existing",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       oldToken,
			"refresh_token":      "refresh-old",
		},
	}})
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: map[string]any{
			"access_token":  newToken,
			"refresh_token": "refresh-new",
		}},
		{Index: 2, Value: map[string]any{"access_token": oldToken}},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Updated != 1 || result.Created != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want first item updated and stale access token created separately", result)
	}
	if len(svc.updatedProviders) != 1 || svc.updatedProviders[0].id != 14 {
		t.Fatalf("updated providers = %+v, want provider 14 updated once", svc.updatedProviders)
	}
	stored, err := svc.GetProvider(context.Background(), 14)
	if err != nil {
		t.Fatalf("GetProvider error = %v", err)
	}
	if got := stored.Credentials["access_token"]; got != newToken {
		t.Fatalf("stored access_token rolled back = %v, want new token", got)
	}
	if got := stored.Credentials["refresh_token"]; got != "refresh-new" {
		t.Fatalf("stored refresh_token = %v, want refresh-new", got)
	}
}

func TestImportCodexSessionsWithRefreshTokenKeepsExistingDedup(t *testing.T) {
	existingToken := buildCodexAccessToken(t, "workspace-1", "user-1", time.Now().Add(time.Hour))
	svc := newCodexImportMemoryAdminService([]provider.Record{{
		ID:       11,
		Name:     "existing",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1",
			"chatgpt_user_id":    "user-1",
			"access_token":       existingToken,
			"refresh_token":      "refresh-old",
		},
	}})
	handler := newCodexImportFixture(svc)
	req := provider.CodexSessionImportRequest{}
	entries := []provider.CodexImportEntry{
		{Index: 1, Value: buildCodexRefreshImportValue(t, "workspace-1", "user-1", "refresh-new")},
	}

	result, err := handler.Import(context.Background(), req, entries)
	if err != nil {
		t.Fatalf("importCodexSessions error = %v", err)
	}
	if result.Created != 0 || result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want one updated provider", result)
	}
	if got := svc.updatedProviders[0].input.Credentials["refresh_token"]; got != "refresh-new" {
		t.Fatalf("updated refresh_token = %v, want refresh-new", got)
	}
}

func buildAgentIdentityImportValue(t *testing.T, runtimeID, providerID, userID, taskID string) map[string]any {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	return map[string]any{
		"auth_mode": "agentIdentity",
		"agent_identity": map[string]any{
			"agent_runtime_id":  runtimeID,
			"agent_private_key": base64.StdEncoding.EncodeToString(der),
			"task_id":           taskID,
			"account_id":        providerID,
			"chatgpt_user_id":   userID,
		},
	}
}

func newCodexImportMemoryAdminService(providers []provider.Record) *codexImportMemoryAdminService {
	stub := newManagementMutationFixture()
	stub.providers = append([]provider.Record(nil), providers...)
	return &codexImportMemoryAdminService{
		managementMutationFixture: stub,
		nextID:                    100,
	}
}

// newCodexImportFixture connects the importer to the in-memory archive and provider store.
func newCodexImportFixture(svc *codexImportMemoryAdminService) *provider.CodexImporter {
	archive := provider.NewArchive(svc, nil, provider.ArchiveOptions{Now: time.Now})
	return provider.NewCodexImporter(svc, archive, codexImportFixtureOptions())
}

func (s *codexImportMemoryAdminService) CreateProvider(ctx context.Context, input *provider.CreateProviderInput) (*provider.Record, error) {
	s.createdProviders = append(s.createdProviders, input)
	if s.createProviderErr != nil {
		return nil, s.createProviderErr
	}
	provider := provider.Record{
		ID:          s.nextID,
		Name:        input.Name,
		Platform:    input.Platform,
		Type:        input.Type,
		Status:      billing.StatusActive,
		Credentials: cloneCodexImportTestMap(input.Credentials),
		Extra:       cloneCodexImportTestMap(input.Extra),
	}
	s.nextID++
	s.providers = append(s.providers, provider)
	return &provider, nil
}

func (s *codexImportMemoryAdminService) UpdateProvider(ctx context.Context, id int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	s.updatedProviders = append(s.updatedProviders, struct {
		id    int64
		input *provider.UpdateProviderInput
	}{id: id, input: input})
	if s.updateProviderErr != nil {
		return nil, s.updateProviderErr
	}
	for idx := range s.providers {
		if s.providers[idx].ID == id {
			s.providers[idx].Credentials = cloneCodexImportTestMap(input.Credentials)
			s.providers[idx].Extra = cloneCodexImportTestMap(input.Extra)
			return &s.providers[idx], nil
		}
	}
	provider := provider.Record{ID: id, Status: billing.StatusActive, Credentials: cloneCodexImportTestMap(input.Credentials)}
	return &provider, nil
}

func (s *codexImportMemoryAdminService) GetProvider(ctx context.Context, id int64) (*provider.Record, error) {
	for idx := range s.providers {
		if s.providers[idx].ID == id {
			return &s.providers[idx], nil
		}
	}
	return s.managementMutationFixture.GetProvider(ctx, id)
}

func buildCodexAccessOnlyImportValue(t *testing.T, providerID, userID string) map[string]any {
	t.Helper()
	return map[string]any{
		"access_token": buildCodexAccessToken(t, providerID, userID, time.Now().Add(time.Hour)),
	}
}

func buildCodexRefreshImportValue(t *testing.T, providerID, userID, refreshToken string) map[string]any {
	t.Helper()
	return map[string]any{
		"access_token":  buildCodexAccessToken(t, providerID, userID, time.Now().Add(time.Hour)),
		"refresh_token": refreshToken,
	}
}

func buildCodexAccessToken(t *testing.T, providerID, userID string, exp time.Time) string {
	t.Helper()
	return buildCodexAccessTokenWithJTI(t, providerID, userID, "", exp)
}

func buildCodexAccessTokenWithJTI(t *testing.T, providerID, userID, jti string, exp time.Time) string {
	t.Helper()
	claims := map[string]any{
		"sub": userID,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": providerID,
		},
	}
	if jti != "" {
		claims["jti"] = jti
	}
	return buildCodexImportTestJWT(t, exp, claims)
}

func cloneCodexImportTestMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	maps.Copy(out, input)
	return out
}

func buildCodexImportTestJWT(t *testing.T, exp time.Time, extraClaims map[string]any) string {
	t.Helper()
	header := map[string]any{
		"alg": "none",
		"typ": "JWT",
	}
	claims := map[string]any{
		"sub": "user-from-sub",
		"exp": exp.Unix(),
		"iat": time.Now().Unix(),
	}
	maps.Copy(claims, extraClaims)
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimBytes) + "."
}

// codexImportFixtureOptions supplies the key parser, clock, and OAuth client ID.
func codexImportFixtureOptions() provider.CodexImportOptions {
	return provider.CodexImportOptions{Now: time.Now, OAuthClientID: openai.ClientID, ValidatePrivateKey: func(value string) error { _, err := openai.ParseAgentIdentityPrivateKey(value); return err }}
}

// ListProviders returns paginated records for the import index.
func (s *codexImportMemoryAdminService) ListProviders(ctx context.Context, page, size int, platform, kind, status, search string, gid int64, privacy, sortBy, order string) ([]provider.Record, int64, error) {
	source := managementListFixture{providers: s.providers}
	return source.ListProviders(ctx, page, size, platform, kind, status, search, gid, privacy, sortBy, order)
}
