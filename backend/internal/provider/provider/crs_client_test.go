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

// crsStaleCredentialRepo models credential replacement during a CRS post-import
// token exchange.
type crsStaleCredentialRepo struct {
	providercore.CRSProviderStore // unimplemented CRS repository methods
	// current is the sequential fixture's stored provider.
	// RefreshTokenWithClientID replaces its credentials before the version-checked
	// write executes.
	current *providercore.Record
}

// crsStaleOAuthClient replaces stored credentials before returning a delayed
// refresh result.
type crsStaleOAuthClient struct {
	OpenAIOAuthClient                         // unimplemented OAuth client methods
	repo              *crsStaleCredentialRepo // mutable refresh comparison repository
	calls             int                     // observed token exchange count
}

// crsDeprecatedExtraProviderRepo retains provider pointers for sequential CRS
// create and update assertions.
type crsDeprecatedExtraProviderRepo struct {
	providercore.CRSProviderStore // unimplemented CRS repository methods
	// providers retains caller-owned record pointers keyed by CRS ID. Sequential
	// tests share those objects when creating, updating, and reading imported
	// state.
	providers map[string]*providercore.Record
	nextID    int64 // largest allocated fixture identifier
}

// crsOpenAIDeprecatedExtraSource supplies a CRS collection and its imported
// credential and Extra maps.
type crsOpenAIDeprecatedExtraSource struct {
	collection  string         // CRS source collection name
	credentials map[string]any // imported source credential fields
	extra       map[string]any // imported source Extra fields
}

// TestCRSClientHTTPContract serves successful and failed login/export
// responses, including bodies beyond read caps. Requests must authenticate and
// execute in order, while failed or incomplete responses return the expected
// diagnostics.
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

// TestCRSClientValidationOrderAndOptionsCopy supplies missing configuration,
// invalid roots, and blank credentials in sequence. Validation must report the
// first applicable error and retain an independent host-list copy.
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

// TestCRSRefreshPreservesConcurrentAdministratorCredentials replaces imported
// credentials during the follow-up token exchange. Import must count creation
// once while conditional refresh keeps the administrator's newer access and
// refresh tokens.
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

// TestCRSSyncDiscardsDeprecatedOpenAILongContextBillingExtra imports several
// old billing-flag representations into new and existing providers.
// Synchronization must remove that obsolete field while retaining unrelated
// source and existing Extra values.
func TestCRSSyncDiscardsDeprecatedOpenAILongContextBillingExtra(t *testing.T) {
	tests := []struct {
		name          string         // obsolete-field scenario label
		collection    string         // CRS source collection name
		credentials   map[string]any // imported source authentication fields
		sourceValue   any            // obsolete value in source
		existingValue any            // obsolete value already stored
		wantAction    string         // expected synchronization action label
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

// UpdateOAuthCredentialsIfUnchanged compares the stored record against the
// exchange's credential version before replacing credentials. Tests initialize
// current and invoke repository operations sequentially; a mismatch returns
// false without storing the delayed result.
//
// The comparison includes ID, platform, type, status, credentials, and proxy
// ID, so an administrator replacement during exchange invalidates its result
// even when the imported row is unchanged. Matching versions delegate to
// UpdateCredentials and return its error with the successful-match flag.
func (r *crsStaleCredentialRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, v providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	c := r.current
	if c.ID != v.ID || c.Platform != v.Platform || c.Type != v.Type || c.Status != v.Status || !reflect.DeepEqual(c.Credentials, v.Credentials) || !reflect.DeepEqual(c.ProxyID, v.ProxyID) {
		return false, nil
	}
	return true, r.UpdateCredentials(ctx, v.ID, credentials)
}

// Create assigns the fixture's provider ID and stores a shallow credential-map
// copy. It mutates the supplied record's ID, while retaining the copy as
// current for later refresh-version comparisons.
func (r *crsStaleCredentialRepo) Create(_ context.Context, v *providercore.Record) error {
	v.ID = 77
	r.current = crsStaleCopy(v)
	return nil
}

// GetByCRSAccountID returns no existing provider for the creation scenario.
func (r *crsStaleCredentialRepo) GetByCRSAccountID(context.Context, string) (*providercore.Record, error) {
	return nil, nil
}

// GetByID returns a copy of the fixture's current record regardless of the
// supplied ID. Credentials get their own top-level map; other mutable fields
// remain shared for this sequential scenario.
func (r *crsStaleCredentialRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	return crsStaleCopy(r.current), nil
}

// UpdateCredentials replaces the fixture's current credentials with the
// supplied map. The map remains shared with the caller, and this sequential
// repository ignores the requested provider ID.
func (r *crsStaleCredentialRepo) UpdateCredentials(_ context.Context, _ int64, v map[string]any) error {
	r.current.Credentials = v
	return nil
}

// crsStaleCopy copies a record and its top-level credential map for refresh
// comparisons. Nil records return nil; nested credential values and other
// record references remain shared with the input.
func crsStaleCopy(v *providercore.Record) *providercore.Record {
	if v == nil {
		return nil
	}
	out := *v
	out.Credentials = map[string]any{}
	maps.Copy(out.Credentials, v.Credentials)
	return &out
}

// RefreshTokenWithClientID counts the exchange and replaces repository
// credentials with administrator values before returning delayed tokens. This
// fixture makes the exchange result stale without adding another refresh call.
func (c *crsStaleOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string, ...openai.OAuthTokenRequestOptions) (*openai.TokenResponse, error) {
	c.calls++
	// Model an administrator replacement while token exchange is still in flight.
	// Conditional persistence must compare against these newer credentials before
	// accepting the late result.
	c.repo.current.Credentials = map[string]any{"access_token": "admin-new-at", "refresh_token": "admin-new-rt"}
	return &openai.TokenResponse{AccessToken: "late-at", RefreshToken: "late-rt", ExpiresIn: 3600}, nil
}

// newCRSDeprecatedExtraProviderRepo indexes nonnil input records by CRS ID and
// retains their pointers. The largest existing provider ID initializes the
// next-create counter, and operations in these fixtures are sequential.
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

// Create assigns the next fixture ID and stores the supplied record pointer
// under its CRS ID. The caller sees the assigned ID and shares that record with
// later repository reads.
func (r *crsDeprecatedExtraProviderRepo) Create(_ context.Context, provider *providercore.Record) error {
	r.nextID++
	provider.ID = r.nextID
	crsID, _ := provider.Extra["crs_account_id"].(string)
	r.providers[crsID] = provider
	return nil
}

// Update retains the supplied provider pointer under its CRS source ID. Reads
// return that same record, and tests use this repository sequentially without
// separate transaction or locking behavior.
func (r *crsDeprecatedExtraProviderRepo) Update(_ context.Context, provider *providercore.Record) error {
	crsID, _ := provider.Extra["crs_account_id"].(string)
	r.providers[crsID] = provider
	return nil
}

// GetByCRSAccountID returns the stored provider pointer for an exact source ID.
// Missing IDs return nil without an error, and callers share mutable record
// state with this sequential fixture.
func (r *crsDeprecatedExtraProviderRepo) GetByCRSAccountID(_ context.Context, crsID string) (*providercore.Record, error) {
	return r.providers[crsID], nil
}

// ListShadowsByParent returns no shadow providers for these CRS synchronization
// fixtures.
func (r *crsDeprecatedExtraProviderRepo) ListShadowsByParent(_ context.Context, _ int64) ([]*providercore.Record, error) {
	return nil, nil
}

// runCRSOpenAIDeprecatedExtraSync serves the supplied source collection through
// local CRS login and export routes. It registers server cleanup, runs
// synchronization against repo, and fails the test on synchronization errors.
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

// UpdateConfiguration delegates storage to the fixture's Update method and
// ignores field-change intent. Calls are sequential and retain the supplied
// record pointer, allowing assertions to inspect the imported Extra result.
func (r *crsDeprecatedExtraProviderRepo) UpdateConfiguration(ctx context.Context, record *providercore.Record, _ providercore.ConfigurationChange) error {
	return r.Update(ctx, record)
}
