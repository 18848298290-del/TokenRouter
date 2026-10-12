package provider

import (
	"context"
	"strings"
	"time"
)

// OAuth provider creation exchanges a PKCE session before asking the
// administrator service to create its record. Successful exchange has already
// removed that session when later creation or group binding fails. A positive
// requested TLS-router ID is stored in Extra so later provider refresh can
// recover the same configured binding.
//
// PAT creation validates whoami metadata, protects authentication fields from
// credential extras and adds import provenance before administrative creation.
// Its Extra input is normalized in place even when later validation fails,
// while merge helpers copy their supported containers for the assembled maps.
// Refresh merges omitted stored fields before PAT cleanup and sends an
// ordinary administrator update; the shared conditional-refresh coordinator is
// a separate entry point. Administrative persistence owns its own row and
// group-binding sequence.

// OpenAIProviderInputError reports an import or refresh input failure to
// administrator handlers.
type OpenAIProviderInputError struct {
	Message string // administrator input validation message
}

// OpenAIProviderOperations supplies administrative reads and writes used after
// OpenAI authorization.
type OpenAIProviderOperations interface {
	// GetProvider returns the selected provider or a lookup error. Successful
	// reads supply a nonnil record for validation, and callers keep its credential
	// observation stable while preparing their subsequent update.
	GetProvider(context.Context, int64) (*Record, error)
	// CreateProvider validates and creates the requested provider and group bindings.
	// Callers keep borrowed input values stable and account for implementation-owned
	// persistence steps that may precede a returned error.
	CreateProvider(context.Context, *CreateProviderInput) (*Record, error)
	// UpdateProvider validates an administrative patch and returns its resulting
	// provider. Callers prepare stable input maps and select the implementation's
	// conditional-write mechanism when an observed credential identity must govern persistence.
	UpdateProvider(context.Context, int64, *UpdateProviderInput) (*Record, error)
}

// OpenAIProviderImport combines shared authorization with administrative
// provider creation and credential updates.
type OpenAIProviderImport struct {
	// Authorization is borrowed and required for these operations. Its activity
	// tracking spans registered exchange work and the subsequent administrative write.
	Authorization *OpenAIAuthorization
	// Admin is borrowed and required for provider reads and writes. It owns
	// durable row creation, credential updates and any subsequent group-binding operations.
	Admin OpenAIProviderOperations
	// ProxyURL is required for PAT requests with a proxy ID. A missing result
	// leaves validation's proxy URL empty; a lookup error aborts before activity registration.
	ProxyURL func(context.Context, int64) (string, bool, error)
}

// OpenAIOAuthProviderCreateInput pairs PKCE exchange proof with requested
// provider configuration.
type OpenAIOAuthProviderCreateInput struct {
	// SessionID, Code and State associate the submitted code and state proof with
	// the immutable PKCE session needed for exchange. The endpoint retains these
	// values together until authorization validates the selected session.
	// SessionID: stored authorization session identity. Code: returned authorization code.
	// State supplies the required nonempty proof compared with the stored session
	// before upstream exchange; missing or mismatched proof propagates an authorization error.
	// RedirectURI overrides the stored
	// redirect when nonempty, and an empty value retains the session selection.
	// Name uses returned email and then the fixed OAuth label when exactly empty;
	// a supplied nonempty name is forwarded to administrative creation as written.
	SessionID, Code, State, RedirectURI, Name string
	// ProxyID is forwarded as an optional exchange override and the persisted
	// creation selection. A recovered session URL alone does not install a ProxyID.
	// TLSFingerprintRouterID selects exchange options and is stored as a provider
	// refresh binding only when its supplied value is positive.
	ProxyID, TLSFingerprintRouterID *int64
	// Concurrency and Priority are forwarded as supplied to administrative creation;
	// this OAuth import path applies none of the PAT path's pointer-based defaults.
	// Concurrency: requested request-slot limit. Priority: requested scheduling priority value.
	Concurrency, Priority int
	GroupIDs              []int64 // requested provider group bindings
}

// OpenAICodexPATCreateInput supplies PAT validation material and requested
// OpenAI provider configuration.
type OpenAICodexPATCreateInput struct {
	// AccessToken supplies PAT validation and fingerprinting material. Default JSON
	// encoding omits this field; String returns a fixed label for the complete input.
	AccessToken string `json:"-"`
	// Name selects the provider label after trimming. Empty input prefers validated
	// email, account ID and user ID in order before the fixed PAT label.
	Name     string
	Notes    *string // optional administrator provider notes
	GroupIDs []int64 // requested provider group bindings
	// ProxyID is looked up before PAT validation and forwarded to creation.
	// A found URL configures validation; an absent lookup leaves its proxy argument empty.
	ProxyID *int64
	// Concurrency defaults to three when absent. Supplied zero remains zero,
	// and negative values fail validation before the PAT callback is invoked.
	Concurrency *int
	// Priority defaults to fifty when absent. Supplied zero remains zero,
	// and negative values fail validation before the PAT callback is invoked.
	Priority *int
	// RateMultiplier is forwarded when supplied. Its negative-only validation accepts
	// zero and does not reject NaN; administrative creation can retain the borrowed pointer.
	RateMultiplier *float64
	// LoadFactor is an integer scoring denominator limited to 10000 here.
	// Administrative creation retains positive pointers and leaves nonpositive values unset.
	LoadFactor *int
	// ExpiresAt is forwarded as Unix seconds. Administrative creation applies
	// a provider expiration only when the supplied timestamp is positive.
	ExpiresAt *int64
	// AutoPauseOnExpired is forwarded to creation, which defaults it to true
	// when absent. This provider expiration setting is separate from OAuth token expiry.
	AutoPauseOnExpired *bool
	// CredentialExtras is sanitized before merging, excluding validation-owned token
	// and identity keys. Supported containers are copied by the import merge helpers.
	CredentialExtras map[string]any `json:"-"`
	// Extra is normalized in place before validation, then merged with provenance.
	// Failure can retain normalization edits, and import provenance wins overlapping keys.
	Extra map[string]any
}

// Error returns the input validation message supplied to the administrator
// handler.
func (e *OpenAIProviderInputError) Error() string { return e.Message }

// NewOpenAIProviderImport retains shared authorization, administrative
// operations and the PAT proxy lookup callback. Supply callable dependencies
// for the operations used; construction starts no work and borrows the
// authorization service's activity lifecycle.
func NewOpenAIProviderImport(auth *OpenAIAuthorization, admin OpenAIProviderOperations, proxy func(context.Context, int64) (string, bool, error)) *OpenAIProviderImport {
	return &OpenAIProviderImport{Authorization: auth, Admin: admin, ProxyURL: proxy}
}

// String returns a fixed creation-input label for ordinary string formatting,
// including no field values in its result. Reflection-based formatting remains
// separate, and callers use this method's label when displaying the request as
// a string.
func (OpenAICodexPATCreateInput) String() string { return "OpenAI PAT provider creation input" }

// RefreshProvider obtains token metadata, merges omitted stored credential
// fields and returns an ordinary administrator update for providerID. Its
// initial read and platform/OAuth-like validation run before authorization
// tracking begins; the authorization service later enforces exact OpenAI OAuth
// type.
//
// Credential shadows are rejected before exchange, because their parent owns
// writable credentials. Accepted exchange and the following update share
// authorization activity tracking, with PAT cleanup applied after inherited
// fields are merged. This update supplies no expected credential version or
// refresh lease; callers coordinate independent credential writers or use the
// conditional-refresh entry point when that comparison is required. Errors can
// follow upstream success, and neither the exchange nor an administrator write
// is rolled back by this method.
func (s *OpenAIProviderImport) RefreshProvider(ctx context.Context, providerID int64, platform string) (*Record, error) {
	provider, err := s.Admin.GetProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}

	if provider.Platform != platform {
		return nil, &OpenAIProviderInputError{Message: "Provider platform does not match OAuth endpoint"}
	}

	if !provider.IsOAuth() {
		return nil, &OpenAIProviderInputError{Message: "Cannot refresh non-OAuth provider credentials"}
	}

	// Spark shadows use parent credentials, so refresh requests must target the parent.
	// Reject the shadow before the upstream exchange would cause a write that its credential guard rejects.
	if provider.IsCredentialShadow() {
		return nil, &OpenAIProviderInputError{Message: "Cannot refresh spark shadow provider; its credentials are managed by the parent provider"}
	}

	ctx, finish, activityErr := s.Authorization.activity.begin(ctx, ErrProbeStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer finish()
	tokenInfo, err := s.Authorization.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	newCredentials := BuildOpenAIProviderCredentials(tokenInfo)

	// Retain omitted account and configuration keys before PAT cleanup removes
	// inherited OAuth-only state, so a partial response does not erase unrelated settings.
	for k, v := range provider.Credentials {
		if _, exists := newCredentials[k]; !exists {
			newCredentials[k] = v
		}
	}
	newCredentials = NormalizeOpenAIPersonalAccessTokenCredentials(provider, tokenInfo, newCredentials)

	// Persist the assembled map through ordinary administrator updates after exchange.
	// No expected credential identity is supplied here; independent writers need separate coordination.
	updatedProvider, err := s.Admin.UpdateProvider(ctx, providerID, &UpdateProviderInput{
		Credentials: newCredentials,
	})
	if err != nil {
		return nil, err
	}

	return updatedProvider, nil
}

// CreateOAuthProvider exchanges req's session proof and delegates creation of
// an OAuth record on the supplied platform. Callers select that platform for
// their endpoint, keep input pointers and slices stable, and provide a nonnil
// context and initialized dependencies.
//
// An exactly empty name uses returned email, then OpenAI OAuth Provider.
// Positive requested TLS-router IDs become an Extra binding; the proxy ID
// passed to creation is req.ProxyID, independently of a proxy recovered from
// session state during exchange. Successful exchange consumes the session
// before creation, so a later administrator or group-binding error does not
// restore that proof. Activity tracking spans the exchange and administrative
// call, while the administrator owns durable row and group operations.
func (s *OpenAIProviderImport) CreateOAuthProvider(ctx context.Context, req OpenAIOAuthProviderCreateInput, platform string) (*Record, error) {
	ctx, finish, activityErr := s.Authorization.activity.begin(ctx, ErrProbeStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer finish()
	tokenInfo, err := s.Authorization.ExchangeCode(ctx, &OpenAIExchangeCodeInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,

		TLSFingerprintRouterID: req.TLSFingerprintRouterID,
	})
	if err != nil {
		return nil, err
	}

	credentials := BuildOpenAIProviderCredentials(tokenInfo)

	// Token email provides an identifiable label when input omitted its name;
	// missing email then selects the fixed OAuth label before administrative creation.
	name := req.Name
	if name == "" && tokenInfo.Email != "" {
		name = tokenInfo.Email
	}
	if name == "" {
		name = "OpenAI OAuth Provider"
	}

	var extra map[string]any
	if req.TLSFingerprintRouterID != nil && *req.TLSFingerprintRouterID > 0 {
		// Retain a positive exchange router as provider configuration so later refresh
		// recovers that binding through GetTLSFingerprintRouterID rather than dropping the selection.
		extra = map[string]any{
			"tls_fingerprint_router_id": *req.TLSFingerprintRouterID,
		}
	}

	// Successful exchange has consumed the session before this creation call;
	// later row or group-binding failures retain that upstream and session-consumption outcome.
	provider, err := s.Admin.CreateProvider(ctx, &CreateProviderInput{
		Name: name,

		Platform: platform,

		Type: "oauth",

		Credentials: credentials,

		Extra: extra,

		ProxyID: req.ProxyID,

		Concurrency: req.Concurrency,

		Priority: req.Priority,

		GroupIDs: req.GroupIDs,
	})
	if err != nil {
		return nil, err
	}

	return provider, nil
}

// CreatePATProvider validates PAT metadata and creates an OpenAI OAuth record
// carrying PAT mode and import provenance. It removes deprecated fields from
// req.Extra before numeric validation, so failed input can still leave that
// map changed; proxy lookup also runs before authorization activity
// registration.
//
// Negative concurrency, priority and rate multipliers are rejected, and load
// factors above 10000 are rejected. Missing concurrency and priority use three
// and fifty, while supplied zero is retained. Protected credential extras
// cannot replace validation-owned authentication fields; provenance overwrites
// matching Extra values. The administrator applies its further validation and
// creation rules and may retain borrowed pointers. A successful validation
// followed by creation error returns that error, with activity tracking
// covering validation and the administrative call.
func (s *OpenAIProviderImport) CreatePATProvider(ctx context.Context, req OpenAICodexPATCreateInput) (*Record, error) {
	DiscardDeprecatedProviderExtra(req.Extra)
	if req.Concurrency != nil && *req.Concurrency < 0 {
		return nil, &OpenAIProviderInputError{Message: "concurrency must be >= 0"}
	}
	if req.Priority != nil && *req.Priority < 0 {
		return nil, &OpenAIProviderInputError{Message: "priority must be >= 0"}
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		return nil, &OpenAIProviderInputError{Message: "rate_multiplier must be >= 0"}
	}
	// Keep the requested load-scoring denominator within the creation limit.
	if req.LoadFactor != nil && *req.LoadFactor > 10000 {
		return nil, &OpenAIProviderInputError{Message: "load_factor must be <= 10000"}
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxyURLValue, found, err := s.ProxyURL(ctx, *req.ProxyID)
		if err != nil {
			return nil, err
		}
		if found {
			proxyURL = proxyURLValue
		}
	}

	ctx, finish, activityErr := s.Authorization.activity.begin(ctx, ErrProbeStopped)
	if activityErr != nil {
		return nil, activityErr
	}
	defer finish()
	tokenInfo, err := s.Authorization.ValidatePersonalAccessToken(ctx, req.AccessToken, proxyURL)
	if err != nil {
		return nil, err
	}

	// Sanitization prevents input extras from replacing whoami-validated authentication
	// fields; merge the allowed configuration only after those protected identities are assembled.
	credentials := MergeCodexImportMap(
		BuildOpenAIProviderCredentials(tokenInfo),
		SanitizeCodexImportCredentialExtras(req.CredentialExtras),
	)
	// Import provenance overwrites overlapping Extra values after normalization,
	// identifying this PAT's source and token digest in the administrative record.
	extra := MergeCodexImportMap(req.Extra, map[string]any{
		"import_source": "codex_personal_access_token",

		"auth_provider": "codex_personal_access_token",

		"imported_at": time.Now().UTC().Format(time.RFC3339),

		"access_token_sha256": CodexTokenFingerprint(req.AccessToken),
	})

	concurrency := 3
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}

	provider, err := s.Admin.CreateProvider(ctx, &CreateProviderInput{
		Name: BuildOpenAICodexPATProviderName(req.Name, tokenInfo),

		Notes: req.Notes,

		Platform: "openai",

		Type: "oauth",

		Credentials: credentials,

		Extra: extra,

		ProxyID: req.ProxyID,

		Concurrency: concurrency,

		Priority: priority,

		RateMultiplier: req.RateMultiplier,

		LoadFactor: req.LoadFactor,

		GroupIDs: req.GroupIDs,

		ExpiresAt: req.ExpiresAt,

		AutoPauseOnExpired: req.AutoPauseOnExpired,
	})
	if err != nil {
		return nil, err
	}

	return provider, nil
}

// BuildOpenAICodexPATProviderName returns a nonblank trimmed name, then the
// first trimmed email, account ID or user ID from available metadata. Missing
// usable identity falls back to Codex PAT Provider, including nil tokenInfo.
func BuildOpenAICodexPATProviderName(name string, tokenInfo *OpenAITokenInfo) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if tokenInfo != nil {
		// Prefer validated human-readable identity before account and user identifiers,
		// so omitted names remain identifiable even when some whoami metadata is unavailable.
		for _, candidate := range []string{tokenInfo.Email, tokenInfo.ChatGPTAccountID, tokenInfo.ChatGPTUserID} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return "Codex PAT Provider"
}
