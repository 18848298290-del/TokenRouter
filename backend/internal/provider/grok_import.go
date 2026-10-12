package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const grokSSOImportConcurrency = 3

var ErrGrokSSOInput = errors.New("sso_tokens is required")

type GrokImportInputError struct{ Message string }

type GrokProviderImportOptions struct {
	Get            func(context.Context, int64) (*Record, error)
	Create         func(context.Context, *CreateProviderInput) (*Record, error)
	Update         func(context.Context, int64, *UpdateProviderInput) (*Record, error)
	Schedule       func(*Record)
	NormalizeToken func(string) string
	RunTask        func(string, func())
	LogError       func(string, ...any)
}
type GrokProviderImport struct {
	Authorization *GrokAuthorization
	Options       GrokProviderImportOptions
}

type GrokOAuthProviderCreateInput struct {
	SessionID, Code, State, RedirectURI, Name string
	ProxyID                                   *int64
	Concurrency, Priority                     int
	GroupIDs                                  []int64
}
type GrokSSOToOAuthRequest struct {
	SSOTokens          []string       `json:"sso_tokens"`
	SSOToken           string         `json:"sso_token"`
	Name               string         `json:"name"`
	Notes              *string        `json:"notes"`
	ProxyID            *int64         `json:"proxy_id"`
	GroupIDs           []int64        `json:"group_ids"`
	Credentials        map[string]any `json:"credentials"`
	Extra              map[string]any `json:"extra"`
	Concurrency        int            `json:"concurrency"`
	LoadFactor         *int           `json:"load_factor"`
	Priority           int            `json:"priority"`
	RateMultiplier     *float64       `json:"rate_multiplier"`
	ExpiresAt          *int64         `json:"expires_at"`
	AutoPauseOnExpired *bool          `json:"auto_pause_on_expired"`
}
type GrokSSOToOAuthItemResult struct {
	Index    int     `json:"index"`
	Name     string  `json:"name,omitempty"`
	Email    string  `json:"email,omitempty"`
	Provider *Record `json:"provider,omitempty"`
	Error    string  `json:"error,omitempty"`
}
type GrokSSOToOAuthResponse struct {
	Created []GrokSSOToOAuthItemResult `json:"created"`
	Failed  []GrokSSOToOAuthItemResult `json:"failed"`
}
type grokSSOImportJob struct {
	index int
	token string
}
type grokSSOImportWorkerResult struct {
	created bool
	item    GrokSSOToOAuthItemResult
}

func (e *GrokImportInputError) Error() string { return e.Message }

func NewGrokProviderImport(auth *GrokAuthorization, options GrokProviderImportOptions) *GrokProviderImport {
	return &GrokProviderImport{Authorization: auth, Options: options}
}

func (h *GrokProviderImport) safeCreateProviderFromSSOToken(ctx context.Context, req GrokSSOToOAuthRequest, token string, index, total int) (result grokSSOImportWorkerResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// Panic values may contain SSO tokens or request data; log the value's type to avoid disclosure.
			h.Options.LogError("grok_sso_import_worker_panic", "index", index, "panic_type", fmt.Sprintf("%T", recovered))
			result = grokSSOImportWorkerResult{
				item: GrokSSOToOAuthItemResult{
					Index: index,
					Error: "internal worker panic",
				},
			}
		}
	}()
	return h.createProviderFromSSOToken(ctx, req, token, index, total)
}

func (h *GrokProviderImport) createProviderFromSSOToken(ctx context.Context, req GrokSSOToOAuthRequest, token string, index, total int) grokSSOImportWorkerResult {
	tokenInfo, err := h.Authorization.ConvertFromSSO(ctx, token, req.ProxyID)
	if err != nil {
		return grokSSOImportWorkerResult{item: GrokSSOToOAuthItemResult{Index: index, Error: GrokSSOImportErrorMessage(err)}}
	}

	credentials := GrokSSOImportCredentials(h.Authorization.BuildProviderCredentials(tokenInfo), req.Credentials)
	name := GrokSSOImportProviderName(req.Name, tokenInfo, index, total)
	expiresAt, autoPauseOnExpired := GrokSSOImportExpiry(req.ExpiresAt, req.AutoPauseOnExpired, tokenInfo)
	provider, err := h.Options.Create(ctx, &CreateProviderInput{
		Name: name,

		Notes: req.Notes,

		Platform: PlatformGrok,

		Type: ProviderTypeOAuth,

		Credentials: credentials,

		Extra: CloneGrokSSOMap(req.Extra),

		ProxyID: req.ProxyID,

		Concurrency: req.Concurrency,

		LoadFactor: req.LoadFactor,

		Priority: req.Priority,

		RateMultiplier: req.RateMultiplier,

		GroupIDs: append([]int64(nil), req.GroupIDs...),

		ExpiresAt: expiresAt,

		AutoPauseOnExpired: autoPauseOnExpired,
	})
	if err != nil {
		return grokSSOImportWorkerResult{item: GrokSSOToOAuthItemResult{Index: index, Name: name, Email: tokenInfo.Email, Error: GrokSSOImportErrorMessage(err)}}
	}
	h.Options.Schedule(provider)
	return grokSSOImportWorkerResult{
		created: true,
		item: GrokSSOToOAuthItemResult{
			Index:    index,
			Name:     name,
			Email:    tokenInfo.Email,
			Provider: provider,
		},
	}
}

// GrokSSOImportCredentials merges exchanged OAuth credentials with import settings.
// Exchanged token fields take precedence; a nonempty requested base_url overrides the built default.
func GrokSSOImportCredentials(built map[string]any, reqCredentials map[string]any) map[string]any {
	// Allow operational settings from the request; authorization secrets remain temporary inputs.
	allowedReqKeys := map[string]struct{}{
		"base_url":      {},
		"model_mapping": {},

		"header_override":         {},
		"header_overrides":        {},
		"header_override_enabled": {},

		"custom_headers": {},
	}
	ops := map[string]any{}
	for k, v := range reqCredentials {
		if _, ok := allowedReqKeys[k]; !ok {
			continue
		}
		if IsSensitiveCredentialKey(k) {
			continue
		}
		ops[k] = v
	}
	credentials := MergeCredentials(ops, built)
	// Remove sensitive fields that older callers may have supplied.
	for k := range credentials {
		if IsSensitiveCredentialKey(k) {
			// Retain token fields produced by BuildProviderCredentials.
			if k == "access_token" || k == "refresh_token" || k == "id_token" {
				continue
			}
			delete(credentials, k)
		}
	}
	if reqBaseURL, ok := reqCredentials["base_url"].(string); ok && strings.TrimSpace(reqBaseURL) != "" {
		credentials["base_url"] = strings.TrimSpace(reqBaseURL)
	}
	return SanitizeStoredCredentials(PlatformGrok, credentials)
}

func GrokSSOImportExpiry(requestExpiresAt *int64, requestAutoPause *bool, tokenInfo *GrokTokenInfo) (*int64, *bool) {
	if tokenInfo == nil || strings.TrimSpace(tokenInfo.RefreshToken) != "" || tokenInfo.ExpiresAt <= 0 {
		return requestExpiresAt, requestAutoPause
	}

	expiresAt := tokenInfo.ExpiresAt
	if requestExpiresAt != nil && *requestExpiresAt > 0 && *requestExpiresAt < expiresAt {
		expiresAt = *requestExpiresAt
	}
	autoPause := true
	return &expiresAt, &autoPause
}

func CloneGrokSSOMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = CloneGrokSSOValue(value)
	}
	return clone
}

func CloneGrokSSOValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return CloneGrokSSOMap(v)
	case []any:
		clone := make([]any, len(v))
		for i, item := range v {
			clone[i] = CloneGrokSSOValue(item)
		}
		return clone
	default:
		return value
	}
}

func (h *GrokProviderImport) normalizeSSOImportTokens(tokens []string, single string) []string {
	items := make([]string, 0, len(tokens)+1)
	if strings.TrimSpace(single) != "" {
		items = append(items, single)
	}
	items = append(items, tokens...)
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		parts := strings.SplitSeq(strings.NewReplacer(",", "\n", "\r", "\n").Replace(item), "\n")
		for token := range parts {
			if token = h.Options.NormalizeToken(token); token == "" {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			result = append(result, token)
		}
	}
	return result
}

func GrokSSOImportProviderName(base string, tokenInfo *GrokTokenInfo, index, total int) string {
	base = strings.TrimSpace(base)
	if base == "" && tokenInfo != nil {
		base = strings.TrimSpace(tokenInfo.Email)
	}
	if base == "" {
		base = "Grok OAuth Provider"
	}
	if total > 1 {
		return base + " #" + strconv.Itoa(index)
	}
	return base
}

func GrokSSOImportErrorMessage(err error) string {
	status := infraerrors.FromError(err)
	if status == nil {
		return ""
	}
	if status.Reason != "" {
		return status.Reason + ": " + status.Message
	}
	return status.Message
}

func (h *GrokProviderImport) CreateFromSSO(ctx context.Context, req GrokSSOToOAuthRequest) (*GrokSSOToOAuthResponse, error) {
	tokens := h.normalizeSSOImportTokens(req.SSOTokens, req.SSOToken)
	if len(tokens) == 0 {
		return nil, ErrGrokSSOInput
	}

	workerCount := min(len(tokens), grokSSOImportConcurrency)
	jobs := make(chan grokSSOImportJob)
	items := make([]grokSSOImportWorkerResult, len(tokens))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		h.Options.RunTask("handler/admin/grok_oauth_handler.go:CreateProvidersFromSSO", func() {
			defer wg.Done()
			for job := range jobs {
				items[job.index] = h.safeCreateProviderFromSSOToken(ctx, req, job.token, job.index+1, len(tokens))
			}
		})
	}
	for i, token := range tokens {
		jobs <- grokSSOImportJob{index: i, token: token}
	}
	close(jobs)
	wg.Wait()

	result := GrokSSOToOAuthResponse{
		Created: make([]GrokSSOToOAuthItemResult, 0, len(tokens)),
		Failed:  make([]GrokSSOToOAuthItemResult, 0),
	}
	for _, item := range items {
		if item.created {
			result.Created = append(result.Created, item.item)
		} else {
			result.Failed = append(result.Failed, item.item)
		}
	}
	return &result, nil
}

func (h *GrokProviderImport) CreateFromOAuth(ctx context.Context, req GrokOAuthProviderCreateInput) (*Record, error) {
	tokenInfo, err := h.Authorization.ExchangeCode(ctx, &GrokExchangeCodeInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,
	})
	if err != nil {
		return nil, err
	}
	credentials := h.Authorization.BuildProviderCredentials(tokenInfo)

	name := strings.TrimSpace(req.Name)
	if name == "" && tokenInfo.Email != "" {
		name = tokenInfo.Email
	}
	if name == "" {
		name = "Grok OAuth Provider"
	}

	provider, err := h.Options.Create(ctx, &CreateProviderInput{
		Name: name,

		Platform: PlatformGrok,

		Type: ProviderTypeOAuth,

		Credentials: credentials,

		ProxyID: req.ProxyID,

		Concurrency: req.Concurrency,

		Priority: req.Priority,

		GroupIDs: req.GroupIDs,
	})
	if err != nil {
		return nil, err
	}
	h.Options.Schedule(provider)
	return provider, nil
}

func (h *GrokProviderImport) RefreshProvider(ctx context.Context, providerID int64) (*Record, error) {
	provider, err := h.Options.Get(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if provider.Platform != PlatformGrok {
		return nil, &GrokImportInputError{Message: "Provider platform does not match Grok OAuth endpoint"}
	}
	if !provider.IsOAuth() {
		return nil, &GrokImportInputError{Message: "Cannot refresh non-OAuth provider credentials"}
	}
	tokenInfo, err := h.Authorization.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}
	newCredentials := h.Authorization.BuildProviderCredentials(tokenInfo)
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)
	if baseURL := strings.TrimSpace(provider.GetCredential("base_url")); baseURL != "" {
		newCredentials["base_url"] = baseURL
	}
	updatedProvider, err := h.Options.Update(ctx, providerID, &UpdateProviderInput{
		Credentials: newCredentials,
	})
	if err != nil {
		return nil, err
	}
	return updatedProvider, nil
}
