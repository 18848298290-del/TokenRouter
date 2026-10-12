package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// crsStaleCredentialRepo holds credentials replaced during token exchange to check rejection of stale
// results.
type crsStaleCredentialRepo struct {
	providercore.CRSProviderStore
	current *providercore.Record
}

type crsStaleOAuthClient struct {
	OpenAIOAuthClient
	repo  *crsStaleCredentialRepo
	calls int
}

type crsDeprecatedExtraProviderRepo struct {
	providercore.CRSProviderStore
	providers map[string]*providercore.Record
	nextID    int64
}

type crsOpenAIDeprecatedExtraSource struct {
	collection  string
	credentials map[string]any
	extra       map[string]any
}

// TestCRSClientHTTPContract checks request order, authentication, errors, and body limits against a
// local server.
func TestCRSClientHTTPContract(t *testing.T) {
	for _, mode := range []string{"success", "login_error", "blank_token", "login_oversize", "export_error", "export_oversize"} {
		t.Run(mode, func(t *testing.T) {
			calls := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/web/auth/login" {
					require.Equal(t, http.MethodPost, r.Method)
					var credentials map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&credentials))
					require.Equal(t, map[string]string{"username": "local", "password": "fixture"}, credentials)
					switch mode {
					case "login_error":
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte("login-denied"))
					case "blank_token":
						_, _ = w.Write([]byte(`{"success":true,"token":" "}`))
					case "login_oversize":
						_, _ = fmt.Fprintf(w, `{"success":true,"padding":"%s","token":"local-token"}`, strings.Repeat("x", 1<<20))
					default:
						_, _ = w.Write([]byte(`{"success":true,"token":"local-token"}`))
					}
					return
				}
				require.Equal(t, "/admin/sync/export-accounts", r.URL.Path)
				require.Equal(t, "true", r.URL.Query().Get("include_secrets"))
				require.Equal(t, "Bearer local-token", r.Header.Get("Authorization"))
				switch mode {
				case "export_error":
					_, _ = w.Write([]byte(`{"success":false,"message":"export-denied"}`))
				case "export_oversize":
					_, _ = fmt.Fprintf(w, `{"success":true,"padding":"%s"}`, strings.Repeat("x", 5<<20))
				default:
					_, _ = w.Write([]byte(`{"success":true,"data":{"geminiApiKeyAccounts":[{"id":"gemini-key"}]}}`))
				}
			}))
			defer server.Close()
			result, err := NewCRSClient(CRSClientOptions{Configured: true, AllowInsecureHTTP: true}).Fetch(context.Background(), server.URL, "local", "fixture")
			switch mode {
			case "success":
				require.NoError(t, err)
				require.Equal(t, "gemini-key", result.Data.GeminiAPIKeyProviders[0].ID)
			case "login_error":
				require.ErrorContains(t, err, "crs login failed: status=403 body=login-denied")
			case "blank_token":
				require.ErrorContains(t, err, "unknown error")
			case "login_oversize":
				require.ErrorContains(t, err, "crs login parse failed")
			case "export_error":
				require.ErrorContains(t, err, "export-denied")
			case "export_oversize":
				require.ErrorContains(t, err, "crs export parse failed")
			}
			if strings.HasPrefix(mode, "login_") || mode == "blank_token" {
				require.Len(t, calls, 1)
			} else {
				require.Len(t, calls, 2)
			}
		})
	}
}

func TestCRSClientValidationOrderAndOptionsCopy(t *testing.T) {
	_, err := NewCRSClient(CRSClientOptions{}).Fetch(context.Background(), "invalid", "", "")
	require.EqualError(t, err, "config is not available")
	_, err = NewCRSClient(CRSClientOptions{Configured: true}).Fetch(context.Background(), "invalid", "", "")
	require.ErrorContains(t, err, "invalid base_url")
	_, err = NewCRSClient(CRSClientOptions{Configured: true}).Fetch(context.Background(), "https://local.test", "", "")
	require.EqualError(t, err, "username and password are required")
	hosts := []string{"allowed.test"}
	client := NewCRSClient(CRSClientOptions{Configured: true, AllowlistEnabled: true, Hosts: hosts})
	hosts[0] = "changed.test"
	require.Equal(t, []string{"allowed.test"}, client.options.Hosts)
}

func TestCRSRefreshPreservesConcurrentAdministratorCredentials(t *testing.T) {
	repo := &crsStaleCredentialRepo{}
	client := &crsStaleOAuthClient{repo: repo}
	deps := &OpenAIAuthorizationDependencies{}
	oauth := newOpenAIAuthorizationForTest(t, nil, client, deps)
	deps.PrivacyFactory = func(string) (*req.Client, error) { return nil, errors.New("local fixture: no enrichment") }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/web/auth/login" {
			_, _ = w.Write([]byte(`{"success":true,"token":"local"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"openaiOAuthAccounts": []any{map[string]any{"id": "source-77", "name": "local", "isActive": true, "schedulable": true, "credentials": map[string]any{"access_token": "source-at", "refresh_token": "source-rt"}}}}}))
	}))
	defer server.Close()
	exchange := &providercore.CRSAuthorization{OpenAI: oauth}
	coordinator := providercore.NewOAuthRefreshAPI(repo, nil, providercore.RefreshOptions{})
	runtime := providercore.NewCRSSync(repo, nil, NewCRSClient(CRSClientOptions{Configured: true, AllowInsecureHTTP: true}), providercore.CRSOptions{Refresh: exchange.Coordinated(coordinator, GeminiTokenCacheKey)})
	result, err := runtime.SyncFromCRS(context.Background(), providercore.SyncFromCRSInput{BaseURL: server.URL, Username: "local", Password: "local"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Equal(t, 1, client.calls)
	require.Equal(t, "admin-new-at", repo.current.Credentials["access_token"])
	require.Equal(t, "admin-new-rt", repo.current.Credentials["refresh_token"])
}

func TestCRSSyncDiscardsDeprecatedOpenAILongContextBillingExtra(t *testing.T) {
	tests := []struct {
		name          string
		collection    string
		credentials   map[string]any
		sourceValue   any
		existingValue any
		wantAction    string
	}{
		{name: "OAuth create discards true", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceValue: true, wantAction: "created"},
		{name: "OAuth create discards malformed", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceValue: "false", wantAction: "created"},
		{name: "API key create discards false", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceValue: false, wantAction: "created"},
		{name: "OAuth update discards existing", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, existingValue: true, wantAction: "updated"},
		{name: "API key update discards source and existing", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceValue: []bool{true}, existingValue: false, wantAction: "updated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const crsID = "crs-openai-1"
			var existing *providercore.Record
			if tt.existingValue != nil {
				providerType := capability.ProviderTypeOAuth
				if tt.collection == "openaiResponsesAccounts" {
					providerType = capability.ProviderTypeAPIKey
				}
				existing = &providercore.Record{
					ID:       41,
					Platform: capability.PlatformOpenAI,
					Type:     providerType,
					Extra: map[string]any{
						"crs_account_id":                      crsID,
						"openai_long_context_billing_enabled": tt.existingValue,
						"existing_preserved":                  true,
					},
				}
			}
			repo := newCRSDeprecatedExtraProviderRepo(existing)
			sourceExtra := map[string]any{"source_preserved": true}
			if tt.sourceValue != nil {
				sourceExtra["openai_long_context_billing_enabled"] = tt.sourceValue
			}
			result := runCRSOpenAIDeprecatedExtraSync(t, repo, crsOpenAIDeprecatedExtraSource{
				collection:  tt.collection,
				credentials: tt.credentials,
				extra:       sourceExtra,
			})

			require.Len(t, result.Items, 1)
			require.Equal(t, tt.wantAction, result.Items[0].Action)
			stored := repo.providers[crsID]
			require.NotNil(t, stored)
			require.NotContains(t, stored.Extra, "openai_long_context_billing_enabled")
			require.Equal(t, true, stored.Extra["source_preserved"])
			if existing != nil {
				require.Equal(t, true, stored.Extra["existing_preserved"])
			}
		})
	}
}

// UpdateOAuthCredentialsIfUnchanged checks the current identity and credential version before applying
// the update.
func (r *crsStaleCredentialRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, v providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	c := r.current
	if c.ID != v.ID || c.Platform != v.Platform || c.Type != v.Type || c.Status != v.Status || !reflect.DeepEqual(c.Credentials, v.Credentials) || !reflect.DeepEqual(c.ProxyID, v.ProxyID) {
		return false, nil
	}
	return true, r.UpdateCredentials(ctx, v.ID, credentials)
}

func (r *crsStaleCredentialRepo) Create(_ context.Context, v *providercore.Record) error {
	v.ID = 77
	r.current = crsStaleCopy(v)
	return nil
}

func (r *crsStaleCredentialRepo) GetByCRSAccountID(context.Context, string) (*providercore.Record, error) {
	return nil, nil
}

func (r *crsStaleCredentialRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	return crsStaleCopy(r.current), nil
}

func (r *crsStaleCredentialRepo) UpdateCredentials(_ context.Context, _ int64, v map[string]any) error {
	r.current.Credentials = v
	return nil
}

func crsStaleCopy(v *providercore.Record) *providercore.Record {
	if v == nil {
		return nil
	}
	out := *v
	out.Credentials = map[string]any{}
	maps.Copy(out.Credentials, v.Credentials)
	return &out
}

func (c *crsStaleOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string, ...openai.OAuthTokenRequestOptions) (*openai.TokenResponse, error) {
	c.calls++
	c.repo.current.Credentials = map[string]any{"access_token": "admin-new-at", "refresh_token": "admin-new-rt"}
	return &openai.TokenResponse{AccessToken: "late-at", RefreshToken: "late-rt", ExpiresIn: 3600}, nil
}

func newCRSDeprecatedExtraProviderRepo(existing ...*providercore.Record) *crsDeprecatedExtraProviderRepo {
	repo := &crsDeprecatedExtraProviderRepo{providers: make(map[string]*providercore.Record)}
	for _, provider := range existing {
		if provider == nil {
			continue
		}
		crsID, _ := provider.Extra["crs_account_id"].(string)
		repo.providers[crsID] = provider
		if provider.ID > repo.nextID {
			repo.nextID = provider.ID
		}
	}
	return repo
}

func (r *crsDeprecatedExtraProviderRepo) Create(_ context.Context, provider *providercore.Record) error {
	r.nextID++
	provider.ID = r.nextID
	crsID, _ := provider.Extra["crs_account_id"].(string)
	r.providers[crsID] = provider
	return nil
}

func (r *crsDeprecatedExtraProviderRepo) Update(_ context.Context, provider *providercore.Record) error {
	crsID, _ := provider.Extra["crs_account_id"].(string)
	r.providers[crsID] = provider
	return nil
}

func (r *crsDeprecatedExtraProviderRepo) GetByCRSAccountID(_ context.Context, crsID string) (*providercore.Record, error) {
	return r.providers[crsID], nil
}

func (r *crsDeprecatedExtraProviderRepo) ListShadowsByParent(_ context.Context, _ int64) ([]*providercore.Record, error) {
	return nil, nil
}

func runCRSOpenAIDeprecatedExtraSync(t *testing.T, repo providercore.CRSProviderStore, source crsOpenAIDeprecatedExtraSource) *providercore.SyncFromCRSResult {
	t.Helper()
	provider := map[string]any{
		"kind":        "openai",
		"id":          "crs-openai-1",
		"name":        "OpenAI CRS",
		"isActive":    true,
		"schedulable": true,
		"credentials": source.credentials,
		"extra":       source.extra,
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/web/auth/login" {
			_, _ = response.Write([]byte(`{"success":true,"token":"admin-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", request.URL.Path)
		require.NoError(t, json.NewEncoder(response).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{source.collection: []any{provider}},
		}))
	}))
	t.Cleanup(server.Close)

	service := providercore.NewCRSSync(repo, nil, NewCRSClient(CRSClientOptions{Configured: true, AllowInsecureHTTP: true}), providercore.CRSOptions{})
	result, err := service.SyncFromCRS(context.Background(), providercore.SyncFromCRSInput{
		BaseURL:  server.URL,
		Username: "admin",
		Password: "password",
	})
	require.NoError(t, err)
	return result
}

// UpdateConfiguration stores the update; fixture calls are sequential.
func (r *crsDeprecatedExtraProviderRepo) UpdateConfiguration(ctx context.Context, record *providercore.Record, _ providercore.ConfigurationChange) error {
	return r.Update(ctx, record)
}
