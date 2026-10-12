package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder creation performs machine preparation in addition to bare credential
// validation. These cases exercise qoder_validation.go through both direct hooks
// and Admin creation/editing, checking token identity requirements, Global/CN
// machine fields, PAT verification and deferred compatibility when only site
// selection changes on a saved PAT record.

// qoderValidationHTTPUpstreamStub records serial transport values for a successful PAT validation response.
type qoderValidationHTTPUpstreamStub struct {
	proxyURL            string // selected PAT proxy URL
	providerID          int64  // selected provider row ID
	providerConcurrency int    // selected transport concurrency value
}

// qoderValidationStore keeps cloned provider records for serial creation and edit fixtures.
type qoderValidationStore struct {
	// AdminStore leaves unconfigured operations on a nil interface. The selected
	// creation/edit cases use Create, GetByID and Update below; other methods panic.
	providercore.AdminStore // unused administration-method substitute
	// providers holds rows by ID through CloneRecord on writes and reads. Supported
	// containers are copied while its other borrowed pointer/value limits remain
	// the core clone helper responsibility; these fixture methods use no synchronization.
	providers map[int64]*providercore.Record
}

// TestValidateQoderCosyCredentialsAcceptsDirectToken supplies a direct security token,
// machine_id and UID to the bare validator. It checks acceptance and that
// machine_token/type preparation is not performed by validation alone.
func TestValidateQoderCosyCredentialsAcceptsDirectToken(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	}

	require.NoError(t, validator.validate(context.Background(), provider))
	require.Empty(t, provider.GetCredential("machine_token"))
	require.Empty(t, provider.GetCredential("machine_type"))
}

// TestCreateQoderDirectTokenProviderPersistsStableMachineIdentity creates a Global
// direct-token provider through Admin hooks. It checks the supplied machine_id
// is retained while the additional Global machine fields are prepared for the stored result.
func TestCreateQoderDirectTokenProviderPersistsStableMachineIdentity(t *testing.T) {
	validator := &qoderCredentialValidator{}
	repo := &qoderValidationStore{}
	svc := newQoderValidationAdmin(repo, validator)

	created, err := svc.CreateProvider(context.Background(), &providercore.CreateProviderInput{
		Name:     "qoder-direct",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	})

	require.NoError(t, err)
	require.Equal(t, "machine-1", created.GetCredential("machine_id"))
	require.NotEmpty(t, created.GetCredential("machine_token"))
	require.NotEmpty(t, created.GetCredential("machine_type"))
}

// TestCreateQoderDirectTokenProviderRejectsMissingMachineID attempts creation with a
// direct token and UID but no machine_id. It checks the machine diagnostic and
// that caller credentials receive none of the machine fields.
func TestCreateQoderDirectTokenProviderRejectsMissingMachineID(t *testing.T) {
	validator := &qoderCredentialValidator{}
	repo := &qoderValidationStore{}
	svc := newQoderValidationAdmin(repo, validator)
	credentials := map[string]any{
		"security_oauth_token": "dt-token",
		"uid":                  "uid-1",
	}

	_, err := svc.CreateProvider(context.Background(), &providercore.CreateProviderInput{
		Name:        "qoder-direct",
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: credentials,
	})

	require.ErrorContains(t, err, "machine_id")
	require.Empty(t, credentials["machine_id"])
	require.Empty(t, credentials["machine_token"])
	require.Empty(t, credentials["machine_type"])
}

// TestCreateQoderPATProviderPersistsStableMachineIdentity creates a Global PAT record
// with an injected verifier. It checks all machine fields are available to that
// verifier and retained on the created provider result.
func TestCreateQoderPATProviderPersistsStableMachineIdentity(t *testing.T) {
	validator := &qoderCredentialValidator{}
	validator.validatePAT = func(_ context.Context, _ *providercore.Record, _ string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		require.NotEmpty(t, machine.MachineID)
		require.NotEmpty(t, machine.MachineToken)
		require.NotEmpty(t, machine.MachineType)
		return &qoder.AuthIdentity{UID: "uid-1", SecurityOauthToken: "dt-token"}, nil
	}
	repo := &qoderValidationStore{}
	svc := newQoderValidationAdmin(repo, validator)

	created, err := svc.CreateProvider(context.Background(), &providercore.CreateProviderInput{
		Name:        "qoder-pat",
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"pat": "pat-123"},
	})

	require.NoError(t, err)
	require.NotEmpty(t, created.GetCredential("machine_id"))
	require.NotEmpty(t, created.GetCredential("machine_token"))
	require.NotEmpty(t, created.GetCredential("machine_type"))
}

// TestCreateQoderCNPATProviderUsesOfficialMachineIdentity creates a CN PAT record
// with an injected site verifier. It checks UUID version/length for machine_id
// and omission of machine_token and machine_type in the result.
func TestCreateQoderCNPATProviderUsesOfficialMachineIdentity(t *testing.T) {
	validator := &qoderCredentialValidator{}
	validator.validateCNPAT = func(_ context.Context, _ *providercore.Record, _ string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		parsedMachineID, err := uuid.Parse(machine.MachineID)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(4), parsedMachineID.Version())
		require.Empty(t, machine.MachineToken)
		require.Empty(t, machine.MachineType)
		return &qoder.AuthIdentity{UID: "uid-cn", SecurityOauthToken: "dt-cn"}, nil
	}
	repo := &qoderValidationStore{}
	svc := newQoderValidationAdmin(repo, validator)

	created, err := svc.CreateProvider(context.Background(), &providercore.CreateProviderInput{
		Name:     "qoder-cn-pat",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"site": "cn",
			"pat":  "pat-cn",
		},
	})

	require.NoError(t, err)
	require.Len(t, created.GetCredential("machine_id"), 36)
	require.Empty(t, created.GetCredential("machine_token"))
	require.Empty(t, created.GetCredential("machine_type"))
}

// TestUpdateQoderDirectTokenProviderPreservesLegacyMachineFallback renames a saved
// direct-token provider with a legacy machine_id. It checks the name changes
// while the identity and absent additional machine fields remain as stored.
func TestUpdateQoderDirectTokenProviderPreservesLegacyMachineFallback(t *testing.T) {
	validator := &qoderCredentialValidator{}
	const providerID int64 = 1202
	repo := &qoderValidationStore{providers: map[int64]*providercore.Record{
		providerID: {
			ID:       providerID,
			Name:     "legacy-qoder",
			Platform: capability.PlatformQoder,
			Type:     capability.ProviderTypeCosy,
			Status:   billing.StatusActive,
			Credentials: map[string]any{
				"security_oauth_token": "dt-token",
				"machine_id":           "legacy-machine",
				"uid":                  "uid-1",
			},
		},
	}}
	svc := newQoderValidationAdmin(repo, validator)
	updatedName := "renamed-qoder"

	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{Name: updatedName})

	require.NoError(t, err)
	require.Equal(t, updatedName, updated.Name)
	require.Equal(t, "legacy-machine", updated.GetCredential("machine_id"))
	require.Empty(t, updated.GetCredential("machine_token"))
	require.Empty(t, updated.GetCredential("machine_type"))
}

// TestValidateQoderCosyCredentialsAcceptsDirectTokenWithAID supplies a direct token
// and machine_id with AID instead of UID. It checks that this accepted identity
// form satisfies the core direct-token validation requirement.
func TestValidateQoderCosyCredentialsAcceptsDirectTokenWithAID(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"aid":                  "aid-1",
		},
	}

	require.NoError(t, validator.validate(context.Background(), provider))
}

// TestValidateQoderCosyCredentialsRejectsDirectTokenWithoutIdentity supplies a direct
// token and machine_id with neither UID nor AID. It checks the diagnostic
// identifies the missing user identity needed for the session.
func TestValidateQoderCosyCredentialsRejectsDirectTokenWithoutIdentity(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
		},
	}

	require.ErrorContains(t, validator.validate(context.Background(), provider), "uid or aid")
}

// TestValidateQoderCosyCredentialsRejectsUnknownSiteAndRefreshMode validates an unknown
// site followed by a supported CN site with unknown refresh mode. It checks
// each unsupported selection returns its own parser diagnostic.
func TestValidateQoderCosyCredentialsRejectsUnknownSiteAndRefreshMode(t *testing.T) {
	validator := &qoderCredentialValidator{}
	baseCredentials := map[string]any{
		"security_oauth_token": "dt-token",
		"machine_id":           "machine-1",
		"uid":                  "uid-1",
	}
	provider := &providercore.Record{Platform: capability.PlatformQoder, Type: capability.ProviderTypeCosy, Credentials: baseCredentials}
	provider.Credentials["site"] = "unknown"
	require.ErrorContains(t, validator.validate(context.Background(), provider), "unsupported site")

	provider.Credentials["site"] = "cn"
	provider.Credentials["refresh_mode"] = "unknown"
	require.ErrorContains(t, validator.validate(context.Background(), provider), "unsupported refresh_mode")
}

// TestValidateQoderCosyCredentialsRejectsMachineIDOnly supplies machine identity with
// no PAT or security token. It checks that the validator reports the missing
// authentication form rather than accepting the machine_id by itself.
func TestValidateQoderCosyCredentialsRejectsMachineIDOnly(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"machine_id": "machine-1"},
	}

	require.ErrorContains(t, validator.validate(context.Background(), provider), "pat or security_oauth_token")
}

// TestValidateQoderCosyCredentialsRejectsNonCosyQoderProviderType supplies a Qoder
// platform with API-key credentials. It checks validation requires COSY provider
// kind before that unsupported authentication form can be saved.
func TestValidateQoderCosyCredentialsRejectsNonCosyQoderProviderType(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "key"},
	}

	require.ErrorContains(t, validator.validate(context.Background(), provider), "require cosy")
}

// TestValidateQoderCosyCredentialsRejectsCosyNonQoderPlatform supplies COSY kind on
// an Anthropic platform record. It checks the diagnostic requires Qoder platform,
// protecting the mutual platform/kind relationship during credential validation.
func TestValidateQoderCosyCredentialsRejectsCosyNonQoderPlatform(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"pat": "pat"},
	}

	require.ErrorContains(t, validator.validate(context.Background(), provider), "requires qoder platform")
}

// TestValidateQoderCosyCredentialsRejectsMachineIDWithAuthDir supplies machine_id and
// a local auth_dir hint without usable authentication. It checks the same missing
// PAT/security-token diagnostic rather than using local credential discovery.
func TestValidateQoderCosyCredentialsRejectsMachineIDWithAuthDir(t *testing.T) {
	validator := &qoderCredentialValidator{}
	provider := &providercore.Record{
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"machine_id": "machine-1",
			"auth_dir":   "/tmp/qoder-auth",
		},
	}

	require.ErrorContains(t, validator.validate(context.Background(), provider), "pat or security_oauth_token")
}

// TestValidateQoderCosyCredentialsExchangesPAT validates a Global PAT through the
// injected exchange hook. It checks one hook invocation while bare validation
// leaves machine fields absent from the provider credential map.
func TestValidateQoderCosyCredentialsExchangesPAT(t *testing.T) {
	validator := &qoderCredentialValidator{}
	calls := 0
	validator.validatePAT = func(ctx context.Context, provider *providercore.Record, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		calls++
		require.Equal(t, "pat-123", pat)
		return &qoder.AuthIdentity{UID: "uid", SecurityOauthToken: "dt-token"}, nil
	}

	provider := &providercore.Record{
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"pat": "pat-123"},
	}

	require.NoError(t, validator.validate(context.Background(), provider))
	require.Equal(t, 1, calls)
	require.Empty(t, provider.GetCredential("machine_id"))
	require.Empty(t, provider.GetCredential("machine_token"))
	require.Empty(t, provider.GetCredential("machine_type"))
}

// TestValidateQoderCosyCredentialsDefersUnchangedPATExchangeForSiteEdit enables deferred
// PAT validation on a CN site selection. It checks acceptance and absent machine
// field mutation, leaving compatibility verification to the later probe.
func TestValidateQoderCosyCredentialsDefersUnchangedPATExchangeForSiteEdit(t *testing.T) {
	validator := &qoderCredentialValidator{}
	validator.validatePAT = func(context.Context, *providercore.Record, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		t.Fatal("站点编辑不应在保存阶段重新交换未变化的 PAT")
		return nil, nil
	}
	provider := &providercore.Record{
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"site": "cn", "pat": "pat-123"},
	}

	err := validator.validateEdit(context.Background(), provider, true)

	require.NoError(t, err)
	require.Empty(t, provider.GetCredential("machine_id"))
	require.Empty(t, provider.GetCredential("machine_token"))
	require.Empty(t, provider.GetCredential("machine_type"))
}

// TestUpdateQoderPATProviderDefersCompatibilityCheckWhenSiteChanges edits only a saved
// PAT provider's site through Admin. It checks the new site and retained PAT
// with no prepared machine_id returned for that compatibility-deferred edit.
func TestUpdateQoderPATProviderDefersCompatibilityCheckWhenSiteChanges(t *testing.T) {
	validator := &qoderCredentialValidator{}
	validator.validatePAT = func(context.Context, *providercore.Record, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		t.Fatal("切换站点应保存原 PAT，并由后续连接测试判断兼容性")
		return nil, nil
	}
	const providerID int64 = 1201
	baseRepo := &qoderValidationStore{providers: map[int64]*providercore.Record{
		providerID: {
			ID:       providerID,
			Platform: capability.PlatformQoder,
			Type:     capability.ProviderTypeCosy,
			Status:   billing.StatusActive,
			Credentials: map[string]any{
				"site": "global",
				"pat":  "pat-123",
			},
		},
	}}
	svc := newQoderValidationAdmin(baseRepo, validator)

	updated, err := svc.UpdateProvider(context.Background(), providerID, &providercore.UpdateProviderInput{
		Credentials: map[string]any{"site": "cn"},
	})

	require.NoError(t, err)
	require.Equal(t, "cn", updated.GetCredential("site"))
	require.Equal(t, "pat-123", updated.GetCredential("pat"))
	require.Empty(t, updated.GetCredential("machine_id"))
}

// TestValidateQoderCosyCredentialsRejectsBadPAT returns a failure from the injected
// Global PAT verifier. It checks the validation error retains that failure,
// so the core cannot treat the rejected credential as accepted.
func TestValidateQoderCosyCredentialsRejectsBadPAT(t *testing.T) {
	validator := &qoderCredentialValidator{}
	validator.validatePAT = func(ctx context.Context, provider *providercore.Record, pat string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, error) {
		return nil, errors.New("bad pat")
	}

	provider := &providercore.Record{
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Credentials: map[string]any{"pat": "pat-123"},
	}

	require.ErrorContains(t, validator.validate(context.Background(), provider), "bad pat")
}

// TestValidateQoderCosyCredentialsPATUsesProviderDoer validates a PAT with a loaded
// proxy and shared transport. It checks the proxy URL, provider ID and concurrency
// reported to that transport for the platform exchange.
func TestValidateQoderCosyCredentialsPATUsesProviderDoer(t *testing.T) {
	validator := &qoderCredentialValidator{}
	upstream := &qoderValidationHTTPUpstreamStub{}
	proxyID := int64(9)
	provider := &providercore.Record{
		ID:          109,
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 5,
		ProxyID:     &proxyID,
		Proxy:       &egress.Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080},
		Credentials: map[string]any{"pat": "pat-123"},
	}

	validator.transport = upstream
	err := validator.validate(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(109), upstream.providerID)
	require.Equal(t, 5, upstream.providerConcurrency)
}

// Do delegates PAT response construction with no TLS profile.
// It records the same provider transport values through DoWithTLS and returns
// a response for the platform client to read and close after verification.
func (s *qoderValidationHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

// DoWithTLS records routing arguments and returns fixed successful PAT identity JSON.
// Profile is unused by this serial fixture, and the response retains its request.
// The platform caller consumes and closes the newly allocated body.
func (s *qoderValidationHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.proxyURL = proxyURL
	s.providerID = providerID
	s.providerConcurrency = providerConcurrency
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"id":"user-1",
			"name":"User",
			"userType":"personal_standard",
			"securityOauthToken":"dt-from-center",
			"refreshToken":"rt-from-center"
		}`)),
		Request: req,
	}, nil
}

// Create retains a cloned fixture record and allocates storage when needed.
// Zero ID is replaced on the input with len(providers)+1 before cloning.
// It uses serial fixture state and returns nil, without checking uniqueness
// against sparse existing IDs.
func (s *qoderValidationStore) Create(_ context.Context, value *providercore.Record) error {
	if s.providers == nil {
		s.providers = make(map[int64]*providercore.Record)
	}
	if value.ID == 0 {
		value.ID = int64(len(s.providers) + 1)
	}
	s.providers[value.ID] = providercore.CloneRecord(value)
	return nil
}

// GetByID returns CloneRecord of the selected stored fixture pointer.
// A missing ID produces nil with no error through the core clone helper.
// Supported containers are copied, while other borrowed pointer values retain
// that helper's ownership limits.
func (s *qoderValidationStore) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	return providercore.CloneRecord(s.providers[id]), nil
}

// Update replaces the row at the input ID with a core clone.
// The fixture map must already exist. It performs no existence check or
// synchronization, and retained unsupported mutable values follow CloneRecord's
// existing sharing rules.
func (s *qoderValidationStore) Update(_ context.Context, value *providercore.Record) error {
	s.providers[value.ID] = providercore.CloneRecord(value)
	return nil
}

// newQoderValidationAdmin binds the serial store to production Qoder credential hooks.
// It supplies the real clock, location reader and UUID seed callback for creation.
// Admin owns the create/edit order while the injected validator supplies site,
// machine preparation and PAT checks.
func newQoderValidationAdmin(store *qoderValidationStore, validator *qoderCredentialValidator) *providercore.Admin {
	return providercore.NewAdmin(store, providercore.AdminOptions{
		Creation:    providercore.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString},
		Credentials: validator.hooks(),
	})
}
