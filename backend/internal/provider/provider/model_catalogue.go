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

// DefaultCodexModelsURL is the OAuth model catalogue endpoint used by administrative synchronization.
const DefaultCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

type upstreamModelEntry struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Model        string          `json:"model"`
	ModelID      string          `json:"modelId"`
	ModelIDSnake string          `json:"model_id"`
	Name         string          `json:"name"`
	Meta         json.RawMessage `json:"_meta"`
}

type upstreamModelEntryMetadata struct {
	ID           string `json:"id"`
	Model        string `json:"model"`
	ModelID      string `json:"modelId"`
	ModelIDSnake string `json:"model_id"`
	Name         string `json:"name"`
}

// ModelCatalogueOptions configures URL validation, response limits, and catalogue endpoints.
type ModelCatalogueOptions struct {
	ValidateURL       func(string) (string, error)
	OperatorValidator grok.BaseURLValidator
	BodyLimit         int64
	CodexModelsURL    string
}

// ModelCatalogue fetches upstream model lists on demand.
type ModelCatalogue struct {
	Options            ModelCatalogueOptions
	Transport          QoderTransport
	Profiles           *egressprovider.TLSProfiles
	ClaudeTokens       *providercore.ClaudeTokenSource
	GrokTokens         *providercore.GrokTokenSource
	GeminiTokens       *providercore.GeminiTokenSource
	AntigravityTokens  *providercore.AntigravityTokenSource
	DefaultGrokBaseURL func(context.Context) string
	Read               func(context.Context, int64) (*providercore.Record, error)
	EnsureTask         func(context.Context, *providercore.Record, string) error
}

func newUpstreamModelSyncConfigError(message string, err error) error {
	return &providercore.UpstreamModelSyncError{Kind: providercore.UpstreamModelSyncErrorConfiguration, Message: message, Err: err}
}

func newUpstreamModelSyncUnsupportedError(message string, err error) error {
	return &providercore.UpstreamModelSyncError{Kind: providercore.UpstreamModelSyncErrorUnsupported, Message: message, Err: err}
}

func newUpstreamModelSyncUpstreamError(message string, err error) error {
	return &providercore.UpstreamModelSyncError{Kind: providercore.UpstreamModelSyncErrorUpstream, Message: message, Err: err}
}

// FetchUpstreamSupportedModels fetches the current model list using the provider's platform API.
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

func (s *ModelCatalogue) buildUpstreamModelsRequest(ctx context.Context, value *providercore.Record) (*http.Request, error) {
	switch {
	case value.Platform == capability.PlatformJev:
		return s.buildJevUpstreamModelsRequest(ctx, value)
	case value.IsGrok():
		return s.buildGrokUpstreamModelsRequest(ctx, value)
	case value.IsOpenAI() || value.IsCNProvider():
		// CN providers use the OpenAI-compatible models endpoint.
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

// buildJevUpstreamModelsRequest builds a model query with the Jev URL and Bearer credentials.
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

// buildGrokUpstreamModelsRequest follows Grok forwarding endpoint rules for model queries.
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
		// Apply CLI headers, but restrict account identity headers to the trusted CLI proxy host.
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
	// Apply provider overrides after defaults and authentication, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)
	return req, nil
}

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
	// Apply provider overrides after defaults and authentication, as forwarding does.
	applyGrokQuotaHeaders(value, req.Header)
	return req, nil
}

// buildOpenAIOAuthUpstreamModelsRequest queries the ChatGPT Codex model catalogue.
// OAuth subscriptions use this endpoint rather than the public Platform API models endpoint.
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

func (s *ModelCatalogue) doUpstreamModelsRequest(req *http.Request, proxyURL string, value *providercore.Record) (*http.Response, error) {
	if s.Profiles == nil {
		return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, nil)
	}
	return s.Transport.DoWithTLS(req, proxyURL, value.ID, value.Concurrency, s.Profiles.ResolveRequestTLS(egress.TLSSelection{Enabled: value.IsTLSFingerprintEnabled(), DirectProfileID: value.GetTLSFingerprintProfileID()}))
}

func upstreamModelsProxyURL(value *providercore.Record) string {
	if value != nil && value.ProxyID != nil && value.Proxy != nil {
		return value.Proxy.URL()
	}
	return ""
}

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

func buildOpenAIModelsURL(base string) string {
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/models")
}

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

func extractUpstreamModelIDs(body []byte) ([]string, error) {
	return extractUpstreamModelIDsWithSelector(body, upstreamModelEntryID)
}

func extractGrokUpstreamModelIDs(body []byte) ([]string, error) {
	return extractUpstreamModelIDsWithSelector(body, grokUpstreamModelEntryID)
}

func extractUpstreamModelIDsWithSelector(body []byte, selectID func(upstreamModelEntry) string) ([]string, error) {
	var response struct {
		Data   []upstreamModelEntry `json:"data"`
		Models []upstreamModelEntry `json:"models"`
	}
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

func upstreamModelEntryID(entry upstreamModelEntry) string {
	modelID := strings.TrimSpace(entry.ID)
	if modelID == "" {
		modelID = strings.TrimSpace(entry.Slug)
	}
	if modelID == "" {
		modelID = strings.TrimSpace(entry.Name)
	}
	return strings.TrimPrefix(modelID, "models/")
}

func grokUpstreamModelEntryID(entry upstreamModelEntry) string {
	candidates := []string{
		entry.Model,
		entry.ModelID,
		entry.ModelIDSnake,
		entry.ID,
	}
	if len(entry.Meta) > 0 {
		var meta upstreamModelEntryMetadata
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
	// Use the display name only when the catalogue provides no protocol model ID.
	candidates = append(candidates, entry.Name)
	for _, candidate := range candidates {
		modelID := strings.TrimSpace(candidate)
		if modelID != "" {
			return strings.TrimPrefix(modelID, "models/")
		}
	}
	return ""
}

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

// buildCodexModelsManifestURL adds client_version to the model catalogue URL.
// appendModelsPath appends the OpenAI models path when requested; OAuth callers pass false.
func buildCodexModelsManifestURL(endpoint string, appendModelsPath bool, clientVersion string) (*url.URL, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if requestURL.Fragment != "" {
		return nil, fmt.Errorf("URL fragments are not supported")
	}
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

func (s *ModelCatalogue) agentHeaders(ctx context.Context, value *providercore.Record) (http.Header, error) {
	headers, _, err := AgentIdentityHeaders(ctx, value, s.EnsureTask)
	return headers, err
}

// extractJevModelIDs reads TypeSafe name fields, retaining path prefixes that are part of the model ID.
func extractJevModelIDs(body []byte) ([]string, error) {
	var response struct {
		Models []struct {
			Name string `json:"name"`
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
