package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/jev"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Upstream model lists use different endpoints and identity fields across
// API-key and OAuth services. OpenAI OAuth reads a Codex manifest, CN providers
// query an OpenAI-format root, Grok can place protocol IDs in metadata, and Jev
// names retain their complete path spelling.
//
// Requests resolve platform authentication and loaded proxy or TLS settings
// before reading a bounded response. Generic model extraction chooses standard
// ID, slug, then name, while Grok uses protocol fields before display labels.
// Both supported object wrappers and top-level arrays feed sorted distinct
// results. Typed synchronization errors distinguish configuration, unsupported
// routes, and upstream failures, and non-success HTTP diagnostics report status
// without copying the upstream response body into the error.

// DefaultCodexModelsURL supplies the OAuth manifest endpoint configured for
// administrative model synchronization and authenticated catalogue queries.
const DefaultCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

// upstreamModelEntry represents alternate identities found in upstream
// catalogue list entries.
type upstreamModelEntry struct {
	// ID, Slug, Model, ModelID, ModelIDSnake, Name, and Meta carry alternate entry
	// identities. Standard selection uses ID, Slug, and Name; Grok consults
	// protocol fields and valid Meta before entry Name.
	ID           string `json:"id"`       // standard catalogue entry identifier
	Slug         string `json:"slug"`     // Codex manifest model slug
	Model        string `json:"model"`    // Grok protocol model identifier
	ModelID      string `json:"modelId"`  // camel-case protocol model identifier
	ModelIDSnake string `json:"model_id"` // snake-case protocol model identifier
	Name         string `json:"name"`     // final catalogue name candidate
	// Meta holds Grok's nested protocol candidates. Invalid JSON or incompatible
	// field types are ignored, leaving the entry's other candidates available.
	Meta json.RawMessage `json:"_meta"`
}

// upstreamModelEntryMetadata represents Grok protocol identities nested under a
// catalogue entry's metadata.
type upstreamModelEntryMetadata struct {
	// Model, ModelID, ModelIDSnake, ID, and Name supply ordered Grok metadata
	// candidates after top-level protocol fields. Metadata Name still precedes the
	// outer entry's display Name.
	ID           string `json:"id"`       // nested catalogue entry identifier
	Model        string `json:"model"`    // nested protocol model identifier
	ModelID      string `json:"modelId"`  // nested camel-case model identifier
	ModelIDSnake string `json:"model_id"` // nested snake-case model identifier
	Name         string `json:"name"`     // nested final name candidate
}

// ModelCatalogueOptions supplies model-query URL validation, response limits,
// and manifest location.
type ModelCatalogueOptions struct {
	ValidateURL       func(string) (string, error) // configured platform root validator
	OperatorValidator grok.BaseURLValidator        // configured Grok operator validator
	// BodyLimit caps accepted model-list bytes and is passed to Antigravity's
	// client. Configure a positive value; generic reads use one extra byte to
	// detect overflow.
	BodyLimit int64
	// CodexModelsURL supplies the OAuth manifest endpoint as configured. The
	// builder retains its path and replaces client_version rather than inserting a
	// default endpoint.
	CodexModelsURL string
}

// ModelCatalogue prepares authenticated platform requests and extracts upstream
// supported model identifiers.
type ModelCatalogue struct {
	Options ModelCatalogueOptions // model-list preparation and limits
	// Transport sends generic model-list queries. Its absence is a configuration
	// error after the Antigravity native-client branch has been considered.
	Transport QoderTransport
	// Profiles optionally selects generic request fingerprints. A nil service
	// passes a nil profile to the shared sender.
	Profiles *egressprovider.TLSProfiles
	// ClaudeTokens optionally fills an absent stored OAuth access token. A present
	// credential is used before invoking this source.
	ClaudeTokens *providercore.ClaudeTokenSource
	// GrokTokens is required for OAuth model queries, including records with a
	// stored token. API-key queries use their own credential directly.
	GrokTokens *providercore.GrokTokenSource
	// GeminiTokens supplies OAuth credentials for supported non-Code-Assist
	// listings. Missing sources cause a configuration error when that path is
	// selected.
	GeminiTokens *providercore.GeminiTokenSource
	// AntigravityTokens supplies native available-model authentication. The
	// Antigravity branch rejects a missing source before creating its native
	// client.
	AntigravityTokens *providercore.AntigravityTokenSource
	// DefaultGrokBaseURL optionally supplies the dynamic OAuth root. API-key
	// preparation and OAuth factories without this callback use their existing
	// platform-root rules.
	DefaultGrokBaseURL func(context.Context) string
	// Read resolves OpenAI shadow credential owners. The manifest builder
	// propagates lookup failures before it creates authenticated request headers.
	Read func(context.Context, int64) (*providercore.Record, error)
	// EnsureTask prepares Agent Identity authentication for OAuth manifests.
	// Configure it before selecting a record that signs task assertions.
	EnsureTask func(context.Context, *providercore.Record, string) error
}

// newUpstreamModelSyncConfigError wraps the supplied message and cause as a
// configuration failure.
func newUpstreamModelSyncConfigError(message string, err error) error {
	return &providercore.UpstreamModelSyncError{Kind: providercore.UpstreamModelSyncErrorConfiguration, Message: message, Err: err}
}

// newUpstreamModelSyncUnsupportedError wraps the supplied message and cause as
// an unsupported-route failure.
func newUpstreamModelSyncUnsupportedError(message string, err error) error {
	return &providercore.UpstreamModelSyncError{Kind: providercore.UpstreamModelSyncErrorUnsupported, Message: message, Err: err}
}

// newUpstreamModelSyncUpstreamError wraps the supplied message and cause as an
// upstream-query failure.
func newUpstreamModelSyncUpstreamError(message string, err error) error {
	return &providercore.UpstreamModelSyncError{Kind: providercore.UpstreamModelSyncErrorUpstream, Message: message, Err: err}
}

// FetchUpstreamSupportedModels queries the record's platform and returns sorted
// distinct model IDs. Configure a positive BodyLimit and required platform
// dependencies. Generic queries return categorized errors for nil inputs,
// unsupported routes, oversized bodies, failed statuses, invalid JSON, and
// empty decoded lists. Native Antigravity rejects a missing model map before
// normalizing its keys, so a map containing only blank keys can return an empty
// slice without an error.
func (s *ModelCatalogue) FetchUpstreamSupportedModels(ctx context.Context, value *providercore.Record) ([]string, error) {
	if s == nil {
		return nil, newUpstreamModelSyncConfigError("Provider test service is not configured", nil)
	}
	if value == nil {
		return nil, newUpstreamModelSyncConfigError("Provider is required", nil)
	}

	if value.Platform == capability.PlatformAntigravity {
		return s.fetchAntigravityOAuthUpstreamModels(ctx, value)
	}

	if s.Transport == nil {
		return nil, newUpstreamModelSyncConfigError("Upstream HTTP client is not configured", nil)
	}

	req, err := s.buildUpstreamModelsRequest(ctx, value)
	if err != nil {
		return nil, err
	}

	proxyURL := upstreamModelsProxyURL(value)
	resp, err := s.doUpstreamModelsRequest(req, proxyURL, value)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to request upstream model list", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyLimit := s.Options.BodyLimit
	// One extra byte distinguishes exact-limit responses from overflow before
	// JSON parsing, so a truncated prefix cannot masquerade as a complete list.
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to read upstream model list", err)
	}
	if int64(len(body)) > bodyLimit {
		return nil, newUpstreamModelSyncUpstreamError("Upstream model list response is too large", fmt.Errorf("response exceeds %d bytes", bodyLimit))
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, newUpstreamModelSyncUpstreamError(
			fmt.Sprintf("Upstream model list request failed with HTTP %d", resp.StatusCode),
			fmt.Errorf("upstream model list returned HTTP %d", resp.StatusCode),
		)
	}

	extractModels := extractUpstreamModelIDs
	if value.Platform == capability.PlatformJev {
		extractModels = extractJevModelIDs
	}
	if value.IsGrok() {
		extractModels = extractGrokUpstreamModelIDs
	}
	models, err := extractModels(body)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Upstream model list response was not valid JSON", err)
	}
	if len(models) == 0 {
		return nil, newUpstreamModelSyncUpstreamError("Upstream returned no supported models", nil)
	}

	return models, nil
}

// buildUpstreamModelsRequest selects the model-list builder for a nonnil
// provider record. CN platforms use OpenAI-format requests, while unknown
// platforms return an unsupported synchronization error before transport
// execution.
func (s *ModelCatalogue) buildUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	switch {
	case value.Platform == capability.PlatformJev:
		return s.buildJevUpstreamModelsRequest(ctx, value)
	case value.IsGrok():
		return s.buildGrokUpstreamModelsRequest(ctx, value)
	case value.IsOpenAI() || value.IsCNProvider():
		// CN inference can use Messages, but its model catalogue still follows OpenAI
		// format. Choose that root before appending the shared models endpoint.
		return s.buildOpenAIUpstreamModelsRequest(ctx, value)
	case value.IsGemini():
		return s.buildGeminiUpstreamModelsRequest(ctx, value)
	case value.IsAnthropic():
		return s.buildAnthropicUpstreamModelsRequest(ctx, value)
	default:
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported platform for upstream model sync: %s", value.Platform), nil,
		)
	}
}

// buildJevUpstreamModelsRequest creates a validated Jev models request with the
// record's bearer API key. It applies enabled provider overrides after
// authentication, and root validation failures become configuration errors.
func (s *ModelCatalogue) buildJevUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	base, err := s.Options.ValidateURL(value.GetJevBaseURL())
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Jev base URL", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jev.EndpointURL(base, "models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+value.GetCredential("api_key"))
	req.Header.Set("Accept", "application/json")
	ApplyProviderHeaderOverrides(value, req.Header)
	return req, nil
}

// buildGrokUpstreamModelsRequest creates a Grok models request using API-key or
// manually acquired OAuth credentials. OAuth requires GrokTokens and applies
// CLI headers; account identity headers are added only for the trusted CLI
// proxy target.
func (s *ModelCatalogue) buildGrokUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	if value == nil {
		return nil, newUpstreamModelSyncConfigError("Provider is required", nil)
	}

	var (
		authToken         string
		normalizedBaseURL string
		isOAuth           = value.IsGrokOAuth()
	)
	switch value.Type {
	case capability.ProviderTypeAPIKey:
		authToken = strings.TrimSpace(value.GetCredential("api_key"))
		if authToken == "" {
			return nil, newUpstreamModelSyncConfigError("No Grok API key is available", nil)
		}

		baseURL := strings.TrimSpace((&GrokQuotaTransport{}).baseURL(ctx, value, false))
		validatedBaseURL, err := s.Options.ValidateURL(baseURL)
		if err != nil {
			return nil, newUpstreamModelSyncConfigError("Invalid Grok base URL", err)
		}
		normalizedBaseURL = validatedBaseURL
	case capability.ProviderTypeOAuth:
		if s.GrokTokens == nil {
			return nil, newUpstreamModelSyncConfigError("Grok token provider is not configured", nil)
		}
		accessToken, err := s.GrokTokens.GetAccessTokenForManualTest(ctx, value)
		if err != nil {
			return nil, newUpstreamModelSyncUpstreamError("Failed to get Grok access token", err)
		}
		authToken = strings.TrimSpace(accessToken)
		if authToken == "" {
			return nil, newUpstreamModelSyncConfigError("No Grok access token is available", nil)
		}

		validator, err := GrokBaseURLValidator(value, s.Options.OperatorValidator)
		if err != nil {
			return nil, newUpstreamModelSyncConfigError("Invalid Grok base URL", err)
		}
		baseURL := (&GrokQuotaTransport{}).baseURL(ctx, value, false)
		if s.DefaultGrokBaseURL != nil {
			baseURL = (&GrokQuotaTransport{DefaultBaseURL: s.DefaultGrokBaseURL}).baseURL(ctx, value, true)
		}
		validatedBaseURL, err := validator(baseURL)
		if err != nil {
			return nil, newUpstreamModelSyncConfigError("Invalid Grok base URL", err)
		}
		normalizedBaseURL = validatedBaseURL
	default:
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported Grok provider type for upstream model sync: %s", value.Type), nil,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildOpenAIModelsURL(normalizedBaseURL), nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Grok model list URL", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	if isOAuth {
		// CLI authentication headers apply to the OAuth route. Add account identity
		// only for the trusted proxy target, so a configured custom root receives no
		// user ID or email.
		grok.ApplyCLIHeaders(req.Header)
		if (grok.MediaCodec{}).IsGrokCLIProxyTarget(req.URL.String()) {
			if userID := strings.TrimSpace(value.GetCredential("sub")); userID != "" {
				req.Header.Set("X-UserID", userID)
			}
			if email := strings.TrimSpace(value.GetCredential("email")); email != "" {
				req.Header.Set("X-Email", email)
			}
		}
	}
	applyGrokQuotaHeaders(value, req.Header)
	return req, nil
}

// buildAnthropicUpstreamModelsRequest creates a validated Messages-platform
// model query. Bedrock and service accounts are unsupported; OAuth prefers its
// stored access token before ClaudeTokens, while API keys use their configured
// root and authentication scheme.
func (s *ModelCatalogue) buildAnthropicUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	if value.IsBedrock() || value.Type == capability.ProviderTypeServiceAccount {
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported Anthropic provider type for upstream model sync: %s", value.Type), nil,
		)
	}

	baseURL := "https://api.anthropic.com"
	authHeaderName := ""
	authHeaderValue := ""
	apiKeyAuthToken := ""
	betaHeader := ""

	if value.IsOAuth() {
		accessToken := strings.TrimSpace(value.GetCredential("access_token"))
		if accessToken == "" && s.ClaudeTokens != nil {
			token, tokenErr := s.ClaudeTokens.GetAccessToken(ctx, value)
			if tokenErr != nil {
				return nil, newUpstreamModelSyncUpstreamError("Failed to get Anthropic access token", tokenErr)
			}
			accessToken = strings.TrimSpace(token)
		}
		if accessToken == "" {
			return nil, newUpstreamModelSyncConfigError("No Anthropic access token is available", nil)
		}
		authHeaderName = "Authorization"
		authHeaderValue = "Bearer " + accessToken
		betaHeader = claude.DefaultBetaHeader
	} else if value.Type == capability.ProviderTypeAPIKey {
		apiKey := strings.TrimSpace(value.GetCredential("api_key"))
		if apiKey == "" {
			return nil, newUpstreamModelSyncConfigError("No Anthropic API key is available", nil)
		}
		baseURL = value.GetBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.anthropic.com"
		}
		apiKeyAuthToken = apiKey
		betaHeader = claude.APIKeyBetaHeader
	} else {
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported Anthropic provider type for upstream model sync: %s", value.Type), nil,
		)
	}

	normalizedBaseURL, err := s.Options.ValidateURL(baseURL)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Anthropic base URL", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildV1ModelsURL(normalizedBaseURL), nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Anthropic model list URL", err)
	}
	for key, value := range claude.DefaultHeaders {
		req.Header.Set(key, value)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", betaHeader)
	if authHeaderName != "" {
		req.Header.Set(authHeaderName, authHeaderValue)
	} else {
		claude.SetAPIKeyAuthHeader(req.Header, value.GetAnthropicAPIKeyAuthScheme() == providercore.AnthropicAPIKeyAuthSchemeAuthorizationBearer, apiKeyAuthToken)
	}
	// Apply allowed overrides after platform defaults and authentication. The
	// shared filter permits intentional client-header replacement while protected
	// credential headers retain their selected values.
	applyGrokQuotaHeaders(value, req.Header)
	return req, nil
}

// buildOpenAIUpstreamModelsRequest delegates OpenAI OAuth to the Codex manifest
// builder and prepares other supported API-key queries. It requires a key,
// resolves the platform's OpenAI-format root, validates it, and applies allowed
// overrides after bearer authentication.
func (s *ModelCatalogue) buildOpenAIUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	if value.IsOpenAIOAuth() {
		return s.buildOpenAIOAuthUpstreamModelsRequest(ctx, value)
	}
	if value.Type != capability.ProviderTypeAPIKey {
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported OpenAI provider type for upstream model sync: %s", value.Type), nil,
		)
	}
	apiKey := strings.TrimSpace(value.GetOpenAIProtocolAPIKey())
	if apiKey == "" {
		return nil, newUpstreamModelSyncConfigError("No OpenAI API key is available", nil)
	}

	// An Anthropic base URL can point at a protocol-specific path.
	// Model synchronization needs the platform and mode's OpenAI-format base URL.
	baseURL := (providercore.ProtocolTarget{Record: value}).GetOpenAIFormatBaseURL()
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com"
	}
	normalizedBaseURL, err := s.Options.ValidateURL(baseURL)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid OpenAI base URL", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildOpenAIModelsURL(normalizedBaseURL), nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid OpenAI model list URL", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	// Apply allowed overrides after platform defaults and authentication. The
	// shared filter permits intentional client-header replacement while protected
	// credential headers retain their selected values.
	applyGrokQuotaHeaders(value, req.Header)
	return req, nil
}

// buildOpenAIOAuthUpstreamModelsRequest creates the Codex manifest request
// using the credential owner of an OAuth record. Shadows resolve before
// authentication and account headers, while the configured manifest path is
// retained and client_version is replaced.
//
// Agent Identity records obtain a signed assertion through EnsureTask; other
// records supply a stored access token. The credential owner's User-Agent
// determines Codex originator and version, and identity enforcement follows
// permitted overrides. This keeps shadow transport configuration separate from
// the parent identity that authenticates the model query.
func (s *ModelCatalogue) buildOpenAIOAuthUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	credentialProvider, err := providercore.ResolveCredentialRecord(ctx, s.Read, value)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Failed to resolve OpenAI provider credentials", err)
	}
	if credentialProvider == nil {
		return nil, newUpstreamModelSyncConfigError("OpenAI provider credentials are unavailable", nil)
	}
	if !credentialProvider.IsOpenAIOAuth() {
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported OpenAI provider type for upstream model sync: %s", credentialProvider.Type), nil,
		)
	}

	modelsURL, err := buildCodexModelsManifestURL(
		s.Options.CodexModelsURL,
		false,
		openai.CodexCanonicalClientVersion(),
	)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid OpenAI Codex model list URL", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL.String(), nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid OpenAI Codex model list request", err)
	}

	if credentialProvider.IsOpenAIAgentIdentity() {
		authHeaders, authErr := s.agentHeaders(ctx, credentialProvider)
		if authErr != nil {
			return nil, newUpstreamModelSyncUpstreamError("Failed to build OpenAI Agent Identity authentication", authErr)
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		accessToken := strings.TrimSpace(credentialProvider.GetOpenAIAccessToken())
		if accessToken == "" {
			return nil, newUpstreamModelSyncConfigError("No OpenAI access token is available", nil)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	identity := openai.ResolveCodexOutboundIdentity(credentialProvider.GetOpenAIUserAgent())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Originator", identity.Originator)
	req.Header.Set("User-Agent", identity.UserAgent)
	req.Header.Set("Version", identity.Version)
	setTestChatGPTHeaders(req.Header, credentialProvider)
	applyGrokQuotaHeaders(credentialProvider, req.Header)
	openai.EnforceCodexIdentityHeadersWithUA(req.Header, credentialProvider.GetOpenAIUserAgent())
	return req, nil
}

// buildGeminiUpstreamModelsRequest builds a validated Gemini model query with
// an API key or token-source OAuth bearer. OAuth records carrying project_id
// are rejected as unsupported Code Assist listings, and other provider types
// return unsupported errors.
func (s *ModelCatalogue) buildGeminiUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	baseURL := value.GetGeminiBaseURL(geminicli.AIStudioBaseURL)
	if strings.TrimSpace(baseURL) == "" {
		baseURL = geminicli.AIStudioBaseURL
	}
	normalizedBaseURL, err := s.Options.ValidateURL(baseURL)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Gemini base URL", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildGeminiModelsURL(normalizedBaseURL), nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Gemini model list URL", err)
	}
	req.Header.Set("Accept", "application/json")

	switch value.Type {
	case capability.ProviderTypeAPIKey:
		apiKey := strings.TrimSpace(value.GetCredential("api_key"))
		if apiKey == "" {
			return nil, newUpstreamModelSyncConfigError("No Gemini API key is available", nil)
		}
		req.Header.Set("x-goog-api-key", apiKey)
	case capability.ProviderTypeOAuth:
		if strings.TrimSpace(value.GetCredential("project_id")) != "" {
			return nil, newUpstreamModelSyncUnsupportedError("Gemini Code Assist model listing is not supported by this sync button", nil)
		}
		if s.GeminiTokens == nil {
			return nil, newUpstreamModelSyncConfigError("Gemini token provider is not configured", nil)
		}
		accessToken, tokenErr := s.GeminiTokens.GetAccessToken(ctx, value)
		if tokenErr != nil {
			return nil, newUpstreamModelSyncUpstreamError("Failed to get Gemini access token", tokenErr)
		}
		accessToken = strings.TrimSpace(accessToken)
		if accessToken == "" {
			return nil, newUpstreamModelSyncConfigError("No Gemini access token is available", nil)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
	default:
		return nil, newUpstreamModelSyncUnsupportedError(
			fmt.Sprintf("Unsupported Gemini provider type for upstream model sync: %s", value.Type), nil,
		)
	}

	return req, nil
}

// fetchAntigravityOAuthUpstreamModels obtains a token and queries the native
// available-models client with the record's loaded proxy and configured body
// limit. Missing tokens, failed clients, and missing model maps return
// categorized errors. Results are trimmed, deduplicated, and sorted; blank-only
// map keys can normalize to an empty successful result.
func (s *ModelCatalogue) fetchAntigravityOAuthUpstreamModels(ctx context.Context, value *providercore.Record) ([]string, error) {
	if s.AntigravityTokens == nil {
		return nil, newUpstreamModelSyncConfigError("Antigravity token provider is not configured", nil)
	}

	accessToken, err := s.AntigravityTokens.GetAccessToken(ctx, value)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to get Antigravity access token", err)
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, newUpstreamModelSyncConfigError("No Antigravity access token is available", nil)
	}

	client, err := antigravity.NewClient(upstreamModelsProxyURL(value))
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Failed to configure Antigravity client", err)
	}
	modelsResp, _, err := client.FetchAvailableModels(
		ctx,
		accessToken,
		strings.TrimSpace(value.GetCredential("project_id")),
		s.Options.BodyLimit,
	)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to fetch Antigravity available models", err)
	}
	if modelsResp == nil || len(modelsResp.Models) == 0 {
		return nil, newUpstreamModelSyncUpstreamError("Upstream returned no supported models", nil)
	}

	models := make([]string, 0, len(modelsResp.Models))
	for modelID := range modelsResp.Models {
		models = append(models, strings.TrimSpace(modelID))
	}
	return dedupeAndSortModelIDs(models), nil
}

// doUpstreamModelsRequest sends the prepared model query through Transport
// using provider identity and concurrency. A nil profile service selects no
// fingerprint; otherwise request TLS settings determine the profile, and
// callers close the returned body.
func (s *ModelCatalogue) doUpstreamModelsRequest(req *http.Request, proxyURL string, value *providercore.Record) (*http.Response, error) {
	if s.Profiles == nil {
		return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, nil)
	}
	return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()}))
}

// upstreamModelsProxyURL returns the loaded proxy URL when both proxy ID and
// relation exist. Nil records or incomplete relations return empty text,
// leaving the query sender to use its direct path.
func upstreamModelsProxyURL(value *providercore.Record) string {
	if value != nil && value.ProxyID != nil && value.Proxy != nil {
		return value.Proxy.URL()
	}
	return ""
}

// buildV1ModelsURL trims the root and appends the Anthropic v1 models suffix.
// Existing v1/models roots stay intact, v1 roots receive models, and other path
// prefixes remain before the appended v1 segment.
func buildV1ModelsURL(base string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(normalized, "/v1/models") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/models"
	}
	return normalized + "/v1/models"
}

// buildOpenAIModelsURL delegates version-aware model endpoint construction to
// the shared HTTP helper.
func buildOpenAIModelsURL(base string) string {
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/models")
}

// buildGeminiModelsURL trims the root and appends Gemini's v1beta models
// suffix. Existing v1beta/models paths remain intact, while v1beta roots
// receive models and other prefixes precede the appended version segment.
func buildGeminiModelsURL(base string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(normalized, "/v1beta/models") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/v1beta") {
		return normalized + "/models"
	}
	return normalized + "/v1beta/models"
}

// extractUpstreamModelIDs extracts standard identities from supported upstream
// catalogue JSON shapes.
func extractUpstreamModelIDs(body []byte) ([]string, error) {
	return extractUpstreamModelIDsWithSelector(body, upstreamModelEntryID)
}

// extractGrokUpstreamModelIDs extracts Grok protocol identities from supported
// upstream catalogue JSON shapes.
func extractGrokUpstreamModelIDs(body []byte) ([]string, error) {
	return extractUpstreamModelIDsWithSelector(body, grokUpstreamModelEntryID)
}

// extractUpstreamModelIDsWithSelector parses object wrappers or a top-level
// entry array and returns sorted distinct selected IDs. The callback chooses
// one identity for each entry; blank selections are removed by final
// normalization.
//
// Object data and models arrays contribute together. When object decoding
// fails, array decoding gets a second opportunity, but a second failure reports
// the original object error. An object with no entries also attempts array form
// before returning an empty result. This accepts vendor response shapes without
// changing the selector's identity precedence.
func extractUpstreamModelIDsWithSelector(body []byte, selectID func(upstreamModelEntry) string) ([]string, error) {
	var response struct {
		// Data and Models are alternate object arrays that can both appear.
		// Extraction appends entries from both before blank removal, deduplication,
		// and sorting.
		Data   []upstreamModelEntry `json:"data"`   // standard model entry array
		Models []upstreamModelEntry `json:"models"` // manifest model entry array
	}
	// A top-level array cannot decode into the object wrapper. Retry that
	// supported shape before returning the original decode failure for an unusable
	// response.
	if err := json.Unmarshal(body, &response); err != nil {
		var arrayResponse []upstreamModelEntry
		if arrayErr := json.Unmarshal(body, &arrayResponse); arrayErr != nil {
			return nil, fmt.Errorf("parse upstream model list: %w", err)
		}

		models := make([]string, 0, len(arrayResponse))
		for _, entry := range arrayResponse {
			models = append(models, selectID(entry))
		}
		return dedupeAndSortModelIDs(models), nil
	}

	models := make([]string, 0, len(response.Data)+len(response.Models))
	for _, entry := range response.Data {
		models = append(models, selectID(entry))
	}
	for _, entry := range response.Models {
		models = append(models, selectID(entry))
	}

	// An empty decoded wrapper contributes no entries. Attempt the array
	// representation as well, and leave the result empty when that alternative is
	// unavailable.
	if len(models) == 0 {
		var arrayResponse []upstreamModelEntry
		if err := json.Unmarshal(body, &arrayResponse); err == nil {
			for _, entry := range arrayResponse {
				models = append(models, selectID(entry))
			}
		}
	}

	return dedupeAndSortModelIDs(models), nil
}

// upstreamModelEntryID chooses a standard catalogue identity by ID, slug, then
// name. Each candidate is trimmed before deciding whether it is usable, and the
// selected value loses one leading models/ prefix.
//
// The precedence accepts standard API IDs, Codex manifest slugs, and Gemini
// resource names while keeping a supplied standard ID ahead of display-oriented
// alternatives. Model and metadata fields are left to the Grok selector. An
// entry with none of these usable fields returns empty text for the collection
// normalizer to discard.
func upstreamModelEntryID(entry upstreamModelEntry) string {
	// Codex manifests can supply slug when ID is absent, and Gemini can use its
	// resource name. Consult those representations after earlier identity fields
	// are blank.
	modelID := strings.TrimSpace(entry.ID)
	if modelID == "" {
		modelID = strings.TrimSpace(entry.Slug)
	}
	if modelID == "" {
		modelID = strings.TrimSpace(entry.Name)
	}
	return strings.TrimPrefix(modelID, "models/")
}

// grokUpstreamModelEntryID prefers Grok protocol fields over catalogue display
// labels. It selects the first trimmed nonempty value from top-level model,
// modelId, model_id, and id, then corresponding valid metadata fields and
// metadata name, with entry name last.
//
// Malformed metadata contributes no candidates, letting usable top-level
// identity survive. A selected models/ prefix is removed once, and an entirely
// empty entry returns empty text. This order keeps protocol model names usable
// when an upstream catalogue also supplies a differing display ID or label.
func grokUpstreamModelEntryID(entry upstreamModelEntry) string {
	candidates := []string{
		entry.Model,
		entry.ModelID,
		entry.ModelIDSnake,
		entry.ID,
	}
	if len(entry.Meta) > 0 {
		var meta upstreamModelEntryMetadata
		// Metadata is optional catalogue evidence. Ignore malformed or incompatible
		// values so a usable outer protocol ID or final display-name candidate can
		// still be selected.
		if err := json.Unmarshal(entry.Meta, &meta); err == nil {
			candidates = append(candidates,
				meta.Model,
				meta.ModelID,
				meta.ModelIDSnake,
				meta.ID,
				meta.Name,
			)
		}
	}
	// Keep entry.Name last: a catalogue may pair a protocol identifier with a
	// separate display label, which is usable after all protocol fields are empty.
	candidates = append(candidates, entry.Name)
	for _, candidate := range candidates {
		modelID := strings.TrimSpace(candidate)
		if modelID != "" {
			return strings.TrimPrefix(modelID, "models/")
		}
	}
	return ""
}

// dedupeAndSortModelIDs returns a new sorted slice of distinct trimmed nonblank
// names. Matching is case-sensitive, input slice contents stay unchanged, and
// an empty input yields an allocated empty result.
func dedupeAndSortModelIDs(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

// buildCodexModelsManifestURL returns a parsed manifest URL with the requested
// client_version and optional models path. Fragments are rejected, while
// unrelated query values survive and an existing client_version is replaced.
//
// The query is detached before endpoint-path construction, then encoded back
// onto the result after its version parameter is set. OAuth callers leave
// appendModelsPath false to retain the configured manifest path. This helper
// parses URL syntax and preserves query identity; callers separately choose a
// trusted endpoint suitable for HTTP execution.
func buildCodexModelsManifestURL(endpoint string, appendModelsPath bool, clientVersion string) (*url.URL, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if requestURL.Fragment != "" {
		return nil, fmt.Errorf("URL fragments are not supported")
	}
	// Detach query values before optional models-path construction, then restore
	// them after client_version replacement so endpoint normalization cannot
	// misassociate query data with the path.
	query := requestURL.Query()
	requestURL.RawQuery = ""
	requestURL.ForceQuery = false
	if appendModelsPath {
		requestURL, err = url.Parse(buildOpenAIModelsURL(requestURL.String()))
		if err != nil {
			return nil, err
		}
	}
	query.Set("client_version", clientVersion)
	requestURL.RawQuery = query.Encode()
	return requestURL, nil
}

// agentHeaders obtains signed Agent Identity headers through the configured
// EnsureTask callback. It forwards preparation errors and discards the signing
// task ID because model-list requests return only authentication headers here.
func (s *ModelCatalogue) agentHeaders(ctx context.Context, value *providercore.Record) (http.Header, error) {
	headers, _, err := AgentIdentityHeaders(ctx, value, s.EnsureTask)
	return headers, err
}

// extractJevModelIDs reads the native models array and returns sorted distinct
// trimmed name values. Complete name paths remain intact, including models/
// prefixes, and JSON decoding errors propagate to synchronization callers.
func extractJevModelIDs(body []byte) ([]string, error) {
	var response struct {
		// Models contains native Jev entries whose name is the full protocol ID.
		Models []struct {
			Name string `json:"name"` // complete Jev model name
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(response.Models))
	for _, model := range response.Models {
		models = append(models, model.Name)
	}
	return dedupeAndSortModelIDs(models), nil
}
