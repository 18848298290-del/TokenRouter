package provider

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// CRSProviderStore supplies configuration, shadow, and credential operations for synchronization.
type CRSProviderStore interface {
	ShadowProxyStore
	Create(context.Context, *Record) error
	GetByCRSAccountID(context.Context, string) (*Record, error)
	ListCRSAccountIDs(context.Context) (map[string]int64, error)
}
type CRSProxyStore interface {
	ListActive(context.Context) ([]egress.Proxy, error)
	Create(context.Context, *egress.Proxy) error
}
type CRSExporter interface {
	Fetch(context.Context, string, string, string) (*transfer.CRSExportResponse, error)
}
type CRSOptions struct {
	Now     func() time.Time
	Warn    func(string, ...any)
	Refresh func(context.Context, *Record) error
}

// CRSSync previews and imports six source categories using injected login and token exchange services.
type CRSSync struct {
	providerRepo CRSProviderStore
	proxyRepo    CRSProxyStore
	exporter     CRSExporter
	options      CRSOptions
}

type SyncFromCRSInput struct {
	BaseURL             string
	Username            string
	Password            string
	SyncProxies         bool
	SelectedProviderIDs []string // Nil creates all entries; an explicit empty list updates existing providers only.
}

type SyncFromCRSItemResult struct {
	CRSAccountID string `json:"crs_account_id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Action       string `json:"action"` // created, updated, failed, or skipped.
	Error        string `json:"error,omitempty"`
}

type SyncFromCRSResult struct {
	Created int                     `json:"created"`
	Updated int                     `json:"updated"`
	Skipped int                     `json:"skipped"`
	Failed  int                     `json:"failed"`
	Items   []SyncFromCRSItemResult `json:"items"`
}

// PreviewFromCRSResult separates new and existing providers in the synchronization preview.
type PreviewFromCRSResult struct {
	NewProviders      []CRSPreviewProvider `json:"new_providers"`
	ExistingProviders []CRSPreviewProvider `json:"existing_providers"`
}

// CRSPreviewProvider contains fields displayed in the import preview.
type CRSPreviewProvider struct {
	CRSAccountID string `json:"crs_account_id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Type         string `json:"type"`
}

func NewCRSSync(providers CRSProviderStore, proxies CRSProxyStore, exporter CRSExporter, options CRSOptions) *CRSSync {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	return &CRSSync{providers, proxies, exporter, options}
}

// refreshOAuthToken attempts refresh after import commits; refresh failure does not fail the imported item.
func (s *CRSSync) refreshOAuthToken(ctx context.Context, value *Record) {
	if s.options.Refresh != nil {
		_ = s.options.Refresh(ctx, value)
	}
}

// GuardCRSShadowParentInvariant requires Spark parents to remain OpenAI OAuth providers.
// CRS looks up globally by crs_account_id, so collisions across source platforms or types could rewrite a parent.
// Check both platform and type to protect shadow credential resolution; delete shadows before changing either.
// On error, the caller skips the update and reports the item as failed.
func GuardCRSShadowParentInvariant(ctx context.Context, repo ShadowProxyStore, existing *Record, newPlatform, newType string) error {
	if existing == nil {
		return nil
	}
	// An OpenAI OAuth target remains a valid shadow parent, so no shadow lookup is needed.
	if newPlatform == PlatformOpenAI && newType == ProviderTypeOAuth {
		return nil
	}
	shadows, err := repo.ListShadowsByParent(ctx, existing.ID)
	if err != nil {
		return fmt.Errorf("check spark shadows for crs update: %w", err)
	}
	if len(shadows) > 0 {
		return fmt.Errorf("cannot change a spark-shadow parent provider to %s/%s; it must stay OpenAI OAuth (delete the shadow first)", newPlatform, newType)
	}
	return nil
}

func (s *CRSSync) SyncFromCRS(ctx context.Context, input SyncFromCRSInput) (*SyncFromCRSResult, error) {
	exported, err := s.exporter.Fetch(ctx, input.BaseURL, input.Username, input.Password)
	if err != nil {
		return nil, err
	}

	now := s.options.Now().UTC().Format(time.RFC3339)

	result := &SyncFromCRSResult{
		Items: make(
			[]SyncFromCRSItemResult,
			0,
			len(exported.Data.ClaudeProviders)+len(exported.Data.ClaudeConsoleProviders)+len(exported.Data.OpenAIOAuthProviders)+len(exported.Data.OpenAIResponsesProviders)+len(exported.Data.GeminiOAuthProviders)+len(exported.Data.GeminiAPIKeyProviders),
		),
	}

	selectedSet := CRSBuildSelectedSet(input.SelectedProviderIDs)

	var proxies []egress.Proxy
	if input.SyncProxies {
		proxies, _ = s.proxyRepo.ListActive(ctx)
	}

	// Claude sources become Anthropic OAuth or setup-token providers.
	for _, src := range exported.Data.ClaudeProviders {
		item := SyncFromCRSItemResult{
			CRSAccountID: src.ID,
			Kind:         src.Kind,
			Name:         src.Name,
		}

		targetType := strings.TrimSpace(src.AuthType)
		if targetType == "" {
			targetType = "oauth"
		}
		if targetType != ProviderTypeOAuth && targetType != ProviderTypeSetupToken {
			item.Action = "skipped"
			item.Error = "unsupported authType: " + targetType
			result.Skipped++
			result.Items = append(result.Items, item)
			continue
		}

		accessToken, _ := src.Credentials["access_token"].(string)
		if strings.TrimSpace(accessToken) == "" {
			item.Action = "failed"
			item.Error = "missing access_token"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		proxyID, err := egress.MatchOrCreateCRSProxy(ctx, s.proxyRepo, input.SyncProxies, &proxies, src.Proxy, fmt.Sprintf("crs-%s", src.Name))
		if err != nil {
			item.Action = "failed"
			item.Error = "proxy sync failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		credentials := CRSSanitizeCredentialsMap(src.Credentials)
		// Remove the trailing /v1 from Claude base URLs.
		CRSCleanBaseURL(credentials, "/v1")
		// Convert RFC3339 expiry values to Unix timestamps.
		if expiresAtStr, ok := credentials["expires_at"].(string); ok && expiresAtStr != "" {
			if t, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
				credentials["expires_at"] = t.Unix()
			}
		}
		// Default warmup interception to false when omitted.
		if _, exists := credentials["intercept_warmup_requests"]; !exists {
			credentials["intercept_warmup_requests"] = false
		}
		priority := CRSClampPriority(src.Priority)
		concurrency := 3
		status := MapCRSStatus(src.IsActive, src.Status)

		// Copy source Extra fields before adding synchronization metadata.
		extra := make(map[string]any)
		if src.Extra != nil {
			maps.Copy(extra, src.Extra)
		}
		extra["crs_account_id"] = src.ID
		extra["crs_kind"] = src.Kind
		extra["crs_synced_at"] = now
		// Copy organization and account IDs from source credentials to Extra.
		if orgUUID, ok := src.Credentials["org_uuid"]; ok {
			extra["org_uuid"] = orgUUID
		}
		if providerUUID, ok := src.Credentials["account_uuid"]; ok {
			extra["account_uuid"] = providerUUID
		}

		existing, err := s.providerRepo.GetByCRSAccountID(ctx, src.ID)
		if err != nil {
			item.Action = "failed"
			item.Error = "db lookup failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		if existing != nil {
			extra = CRSMergeMap(existing.Extra, extra)
			credentials = CRSMergeMap(existing.Credentials, credentials)
		}
		ReconcileCRSOllamaCloudUsageExtra(existing, PlatformAnthropic, targetType, credentials, extra)

		if existing == nil {
			if !CRSShouldCreateProvider(src.ID, selectedSet) {
				item.Action = "skipped"
				item.Error = "not selected"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			provider := &Record{
				Name:        CRSDefaultName(src.Name, src.ID),
				Platform:    PlatformAnthropic,
				Type:        targetType,
				Credentials: credentials,
				Extra:       CloneValues(extra),
				ProxyID:     proxyID,
				Concurrency: concurrency,
				Priority:    priority,
				Status:      status,
				Schedulable: src.Schedulable,
			}
			if err := s.providerRepo.Create(ctx, provider); err != nil {
				item.Action = "failed"
				item.Error = "create failed: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			// Refresh tokens after successful creation on a best-effort basis.
			if targetType == ProviderTypeOAuth {
				s.refreshOAuthToken(ctx, provider)
			}
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// Protect existing OpenAI shadow parents from cross-platform CRS ID collisions.
		if gerr := GuardCRSShadowParentInvariant(ctx, s.providerRepo, existing, PlatformAnthropic, targetType); gerr != nil {
			item.Action = "failed"
			item.Error = gerr.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		// Apply source configuration to the existing provider.
		existing.Extra = CloneValues(extra)
		existing.Name = CRSDefaultName(src.Name, src.ID)
		existing.Platform = PlatformAnthropic
		existing.Type = targetType
		existing.Credentials = credentials
		if proxyID != nil {
			existing.ProxyID = proxyID
		}
		existing.Concurrency = concurrency
		existing.Priority = priority
		existing.Status = status
		existing.Schedulable = src.Schedulable

		if err := WriteConfiguration(ctx, s.providerRepo, existing, CRSSyncConfiguration(proxyID != nil)); err != nil {
			item.Action = "failed"
			item.Error = "update failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		// Refresh tokens after the configuration write on a best-effort basis.
		if targetType == ProviderTypeOAuth {
			s.refreshOAuthToken(ctx, existing)
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// Claude Console sources become Anthropic API Key providers.
	for _, src := range exported.Data.ClaudeConsoleProviders {
		item := SyncFromCRSItemResult{
			CRSAccountID: src.ID,
			Kind:         src.Kind,
			Name:         src.Name,
		}

		apiKey, _ := src.Credentials["api_key"].(string)
		if strings.TrimSpace(apiKey) == "" {
			item.Action = "failed"
			item.Error = "missing api_key"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		proxyID, err := egress.MatchOrCreateCRSProxy(ctx, s.proxyRepo, input.SyncProxies, &proxies, src.Proxy, fmt.Sprintf("crs-%s", src.Name))
		if err != nil {
			item.Action = "failed"
			item.Error = "proxy sync failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		credentials := CRSSanitizeCredentialsMap(src.Credentials)
		priority := CRSClampPriority(src.Priority)
		concurrency := 3
		if src.MaxConcurrentTasks > 0 {
			concurrency = src.MaxConcurrentTasks
		}
		status := MapCRSStatus(src.IsActive, src.Status)

		extra := map[string]any{
			"crs_account_id": src.ID,
			"crs_kind":       src.Kind,
			"crs_synced_at":  now,
		}

		existing, err := s.providerRepo.GetByCRSAccountID(ctx, src.ID)
		if err != nil {
			item.Action = "failed"
			item.Error = "db lookup failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		if existing != nil {
			extra = CRSMergeMap(existing.Extra, extra)
			credentials = CRSMergeMap(existing.Credentials, credentials)
		}
		ReconcileCRSOllamaCloudUsageExtra(existing, PlatformAnthropic, ProviderTypeAPIKey, credentials, extra)

		if existing == nil {
			if !CRSShouldCreateProvider(src.ID, selectedSet) {
				item.Action = "skipped"
				item.Error = "not selected"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			provider := &Record{
				Name:        CRSDefaultName(src.Name, src.ID),
				Platform:    PlatformAnthropic,
				Type:        ProviderTypeAPIKey,
				Credentials: credentials,
				Extra:       CloneValues(extra),
				ProxyID:     proxyID,
				Concurrency: concurrency,
				Priority:    priority,
				Status:      status,
				Schedulable: src.Schedulable,
			}
			if err := s.providerRepo.Create(ctx, provider); err != nil {
				item.Action = "failed"
				item.Error = "create failed: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// Protect existing OpenAI shadow parents from cross-platform CRS ID collisions.
		if gerr := GuardCRSShadowParentInvariant(ctx, s.providerRepo, existing, PlatformAnthropic, ProviderTypeAPIKey); gerr != nil {
			item.Action = "failed"
			item.Error = gerr.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		existing.Extra = CloneValues(extra)
		existing.Name = CRSDefaultName(src.Name, src.ID)
		existing.Platform = PlatformAnthropic
		existing.Type = ProviderTypeAPIKey
		existing.Credentials = credentials
		if proxyID != nil {
			existing.ProxyID = proxyID
		}
		existing.Concurrency = concurrency
		existing.Priority = priority
		existing.Status = status
		existing.Schedulable = src.Schedulable

		if err := WriteConfiguration(ctx, s.providerRepo, existing, CRSSyncConfiguration(proxyID != nil)); err != nil {
			item.Action = "failed"
			item.Error = "update failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// OpenAI OAuth sources remain OAuth providers.
	for _, src := range exported.Data.OpenAIOAuthProviders {
		item := SyncFromCRSItemResult{
			CRSAccountID: src.ID,
			Kind:         src.Kind,
			Name:         src.Name,
		}

		accessToken, _ := src.Credentials["access_token"].(string)
		if strings.TrimSpace(accessToken) == "" {
			item.Action = "failed"
			item.Error = "missing access_token"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		proxyID, err := egress.MatchOrCreateCRSProxy(ctx, s.proxyRepo,
			input.SyncProxies,
			&proxies,
			src.Proxy,
			fmt.Sprintf("crs-%s", src.Name),
		)
		if err != nil {
			item.Action = "failed"
			item.Error = "proxy sync failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		credentials := CRSSanitizeCredentialsMap(src.Credentials)
		// Default a missing or blank token type to Bearer.
		if v, ok := credentials["token_type"].(string); !ok || strings.TrimSpace(v) == "" {
			credentials["token_type"] = "Bearer"
		}
		// Convert RFC3339 expiry values to Unix timestamps.
		if expiresAtStr, ok := credentials["expires_at"].(string); ok && expiresAtStr != "" {
			if t, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
				credentials["expires_at"] = t.Unix()
			}
		}
		priority := CRSClampPriority(src.Priority)
		concurrency := 3
		status := MapCRSStatus(src.IsActive, src.Status)

		// Copy source Extra fields before adding synchronization metadata.
		extra := make(map[string]any)
		if src.Extra != nil {
			maps.Copy(extra, src.Extra)
		}
		extra["crs_account_id"] = src.ID
		extra["crs_kind"] = src.Kind
		extra["crs_synced_at"] = now
		// Expose the source crs_email as email.
		if crsEmail, ok := src.Extra["crs_email"]; ok {
			extra["email"] = crsEmail
		}

		existing, err := s.providerRepo.GetByCRSAccountID(ctx, src.ID)
		if err != nil {
			item.Action = "failed"
			item.Error = "db lookup failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		var existingExtra map[string]any
		if existing != nil {
			existingExtra = existing.Extra
		}
		extra = CRSMergeMap(existingExtra, extra)
		DiscardDeprecatedExtra(extra)
		if existing != nil {
			credentials = CRSMergeMap(existing.Credentials, credentials)
		}
		ReconcileCRSOllamaCloudUsageExtra(existing, PlatformOpenAI, ProviderTypeOAuth, credentials, extra)

		if existing == nil {
			if !CRSShouldCreateProvider(src.ID, selectedSet) {
				item.Action = "skipped"
				item.Error = "not selected"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			provider := &Record{
				Name:        CRSDefaultName(src.Name, src.ID),
				Platform:    PlatformOpenAI,
				Type:        ProviderTypeOAuth,
				Credentials: credentials,
				Extra:       CloneValues(extra),
				ProxyID:     proxyID,
				Concurrency: concurrency,
				Priority:    priority,
				Status:      status,
				Schedulable: src.Schedulable,
			}
			if err := s.providerRepo.Create(ctx, provider); err != nil {
				item.Action = "failed"
				item.Error = "create failed: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			// Refresh tokens after successful creation on a best-effort basis.
			s.refreshOAuthToken(ctx, provider)
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		existing.Extra = CloneValues(extra)
		existing.Name = CRSDefaultName(src.Name, src.ID)
		existing.Platform = PlatformOpenAI
		existing.Type = ProviderTypeOAuth
		existing.Credentials = credentials
		if proxyID != nil {
			existing.ProxyID = proxyID
		}
		existing.Concurrency = concurrency
		existing.Priority = priority
		existing.Status = status
		existing.Schedulable = src.Schedulable

		if err := WriteConfiguration(ctx, s.providerRepo, existing, CRSSyncConfiguration(proxyID != nil)); err != nil {
			item.Action = "failed"
			item.Error = "update failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		// Refresh tokens after the configuration write on a best-effort basis.
		s.refreshOAuthToken(ctx, existing)

		// Propagate the parent's proxy to Spark shadows using the same inheritance rule as admin edits.
		// The parent write has committed; warn on propagation failure while keeping the item successful.
		if perr := PropagateProviderProxyToShadows(ctx, s.providerRepo, existing.ID, existing.ProxyID); perr != nil {
			s.options.Warn("crs_sync_propagate_proxy_to_shadows_failed", "provider_id", existing.ID, "error", perr)
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// OpenAI Responses sources become API Key providers.
	for _, src := range exported.Data.OpenAIResponsesProviders {
		item := SyncFromCRSItemResult{
			CRSAccountID: src.ID,
			Kind:         src.Kind,
			Name:         src.Name,
		}

		apiKey, _ := src.Credentials["api_key"].(string)
		if strings.TrimSpace(apiKey) == "" {
			item.Action = "failed"
			item.Error = "missing api_key"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		if baseURL, ok := src.Credentials["base_url"].(string); !ok || strings.TrimSpace(baseURL) == "" {
			src.Credentials["base_url"] = "https://api.openai.com"
		}
		// Remove the trailing /v1 from OpenAI base URLs.
		CRSCleanBaseURL(src.Credentials, "/v1")

		proxyID, err := egress.MatchOrCreateCRSProxy(ctx, s.proxyRepo,
			input.SyncProxies,
			&proxies,
			src.Proxy,
			fmt.Sprintf("crs-%s", src.Name),
		)
		if err != nil {
			item.Action = "failed"
			item.Error = "proxy sync failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		credentials := CRSSanitizeCredentialsMap(src.Credentials)
		priority := CRSClampPriority(src.Priority)
		concurrency := 3
		status := MapCRSStatus(src.IsActive, src.Status)

		extra := make(map[string]any, len(src.Extra)+3)
		maps.Copy(extra, src.Extra)
		extra["crs_account_id"] = src.ID
		extra["crs_kind"] = src.Kind
		extra["crs_synced_at"] = now

		existing, err := s.providerRepo.GetByCRSAccountID(ctx, src.ID)
		if err != nil {
			item.Action = "failed"
			item.Error = "db lookup failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		var existingExtra map[string]any
		if existing != nil {
			existingExtra = existing.Extra
		}
		extra = CRSMergeMap(existingExtra, extra)
		DiscardDeprecatedExtra(extra)
		if existing != nil {
			credentials = CRSMergeMap(existing.Credentials, credentials)
		}
		ReconcileCRSOllamaCloudUsageExtra(existing, PlatformOpenAI, ProviderTypeAPIKey, credentials, extra)

		if existing == nil {
			if !CRSShouldCreateProvider(src.ID, selectedSet) {
				item.Action = "skipped"
				item.Error = "not selected"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			provider := &Record{
				Name:        CRSDefaultName(src.Name, src.ID),
				Platform:    PlatformOpenAI,
				Type:        ProviderTypeAPIKey,
				Credentials: credentials,
				Extra:       CloneValues(extra),
				ProxyID:     proxyID,
				Concurrency: concurrency,
				Priority:    priority,
				Status:      status,
				Schedulable: src.Schedulable,
			}
			if err := s.providerRepo.Create(ctx, provider); err != nil {
				item.Action = "failed"
				item.Error = "create failed: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// This API Key conversion would invalidate an OpenAI OAuth shadow parent.
		// Require shadows to be deleted before changing their parent's credential type.
		if gerr := GuardCRSShadowParentInvariant(ctx, s.providerRepo, existing, PlatformOpenAI, ProviderTypeAPIKey); gerr != nil {
			item.Action = "failed"
			item.Error = gerr.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		existing.Extra = CloneValues(extra)
		existing.Name = CRSDefaultName(src.Name, src.ID)
		existing.Platform = PlatformOpenAI
		existing.Type = ProviderTypeAPIKey
		existing.Credentials = credentials
		if proxyID != nil {
			existing.ProxyID = proxyID
		}
		existing.Concurrency = concurrency
		existing.Priority = priority
		existing.Status = status
		existing.Schedulable = src.Schedulable

		if err := WriteConfiguration(ctx, s.providerRepo, existing, CRSSyncConfiguration(proxyID != nil)); err != nil {
			item.Action = "failed"
			item.Error = "update failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// Gemini OAuth sources remain OAuth providers.
	for _, src := range exported.Data.GeminiOAuthProviders {
		item := SyncFromCRSItemResult{
			CRSAccountID: src.ID,
			Kind:         src.Kind,
			Name:         src.Name,
		}

		refreshToken, _ := src.Credentials["refresh_token"].(string)
		if strings.TrimSpace(refreshToken) == "" {
			item.Action = "failed"
			item.Error = "missing refresh_token"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		proxyID, err := egress.MatchOrCreateCRSProxy(ctx, s.proxyRepo, input.SyncProxies, &proxies, src.Proxy, fmt.Sprintf("crs-%s", src.Name))
		if err != nil {
			item.Action = "failed"
			item.Error = "proxy sync failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		credentials := CRSSanitizeCredentialsMap(src.Credentials)
		if v, ok := credentials["token_type"].(string); !ok || strings.TrimSpace(v) == "" {
			credentials["token_type"] = "Bearer"
		}
		// Convert expiry to Gemini's Unix-seconds string representation.
		if expiresAtStr, ok := credentials["expires_at"].(string); ok && strings.TrimSpace(expiresAtStr) != "" {
			if t, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
				credentials["expires_at"] = strconv.FormatInt(t.Unix(), 10)
			}
		}

		extra := make(map[string]any)
		if src.Extra != nil {
			maps.Copy(extra, src.Extra)
		}
		extra["crs_account_id"] = src.ID
		extra["crs_kind"] = src.Kind
		extra["crs_synced_at"] = now

		existing, err := s.providerRepo.GetByCRSAccountID(ctx, src.ID)
		if err != nil {
			item.Action = "failed"
			item.Error = "db lookup failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		if existing != nil {
			extra = CRSMergeMap(existing.Extra, extra)
			credentials = CRSMergeMap(existing.Credentials, credentials)
		}
		ReconcileCRSOllamaCloudUsageExtra(existing, PlatformGemini, ProviderTypeOAuth, credentials, extra)

		if existing == nil {
			if !CRSShouldCreateProvider(src.ID, selectedSet) {
				item.Action = "skipped"
				item.Error = "not selected"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			provider := &Record{
				Name:        CRSDefaultName(src.Name, src.ID),
				Platform:    PlatformGemini,
				Type:        ProviderTypeOAuth,
				Credentials: credentials,
				Extra:       CloneValues(extra),
				ProxyID:     proxyID,
				Concurrency: 3,
				Priority:    CRSClampPriority(src.Priority),
				Status:      MapCRSStatus(src.IsActive, src.Status),
				Schedulable: src.Schedulable,
			}
			if err := s.providerRepo.Create(ctx, provider); err != nil {
				item.Action = "failed"
				item.Error = "create failed: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			s.refreshOAuthToken(ctx, provider)
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// Protect existing OpenAI shadow parents from cross-platform CRS ID collisions.
		if gerr := GuardCRSShadowParentInvariant(ctx, s.providerRepo, existing, PlatformGemini, ProviderTypeOAuth); gerr != nil {
			item.Action = "failed"
			item.Error = gerr.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		existing.Extra = CloneValues(extra)
		existing.Name = CRSDefaultName(src.Name, src.ID)
		existing.Platform = PlatformGemini
		existing.Type = ProviderTypeOAuth
		existing.Credentials = credentials
		if proxyID != nil {
			existing.ProxyID = proxyID
		}
		existing.Concurrency = 3
		existing.Priority = CRSClampPriority(src.Priority)
		existing.Status = MapCRSStatus(src.IsActive, src.Status)
		existing.Schedulable = src.Schedulable

		if err := WriteConfiguration(ctx, s.providerRepo, existing, CRSSyncConfiguration(proxyID != nil)); err != nil {
			item.Action = "failed"
			item.Error = "update failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		s.refreshOAuthToken(ctx, existing)

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// Gemini API Key sources remain API Key providers.
	for _, src := range exported.Data.GeminiAPIKeyProviders {
		item := SyncFromCRSItemResult{
			CRSAccountID: src.ID,
			Kind:         src.Kind,
			Name:         src.Name,
		}

		apiKey, _ := src.Credentials["api_key"].(string)
		if strings.TrimSpace(apiKey) == "" {
			item.Action = "failed"
			item.Error = "missing api_key"
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		proxyID, err := egress.MatchOrCreateCRSProxy(ctx, s.proxyRepo, input.SyncProxies, &proxies, src.Proxy, fmt.Sprintf("crs-%s", src.Name))
		if err != nil {
			item.Action = "failed"
			item.Error = "proxy sync failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		credentials := CRSSanitizeCredentialsMap(src.Credentials)
		if baseURL, ok := credentials["base_url"].(string); !ok || strings.TrimSpace(baseURL) == "" {
			credentials["base_url"] = "https://generativelanguage.googleapis.com"
		}

		extra := make(map[string]any)
		if src.Extra != nil {
			maps.Copy(extra, src.Extra)
		}
		extra["crs_account_id"] = src.ID
		extra["crs_kind"] = src.Kind
		extra["crs_synced_at"] = now

		existing, err := s.providerRepo.GetByCRSAccountID(ctx, src.ID)
		if err != nil {
			item.Action = "failed"
			item.Error = "db lookup failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}
		if existing != nil {
			extra = CRSMergeMap(existing.Extra, extra)
			credentials = CRSMergeMap(existing.Credentials, credentials)
		}
		ReconcileCRSOllamaCloudUsageExtra(existing, PlatformGemini, ProviderTypeAPIKey, credentials, extra)

		if existing == nil {
			if !CRSShouldCreateProvider(src.ID, selectedSet) {
				item.Action = "skipped"
				item.Error = "not selected"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			provider := &Record{
				Name:        CRSDefaultName(src.Name, src.ID),
				Platform:    PlatformGemini,
				Type:        ProviderTypeAPIKey,
				Credentials: credentials,
				Extra:       CloneValues(extra),
				ProxyID:     proxyID,
				Concurrency: 3,
				Priority:    CRSClampPriority(src.Priority),
				Status:      MapCRSStatus(src.IsActive, src.Status),
				Schedulable: src.Schedulable,
			}
			if err := s.providerRepo.Create(ctx, provider); err != nil {
				item.Action = "failed"
				item.Error = "create failed: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// Protect existing OpenAI shadow parents from cross-platform CRS ID collisions.
		if gerr := GuardCRSShadowParentInvariant(ctx, s.providerRepo, existing, PlatformGemini, ProviderTypeAPIKey); gerr != nil {
			item.Action = "failed"
			item.Error = gerr.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		existing.Extra = CloneValues(extra)
		existing.Name = CRSDefaultName(src.Name, src.ID)
		existing.Platform = PlatformGemini
		existing.Type = ProviderTypeAPIKey
		existing.Credentials = credentials
		if proxyID != nil {
			existing.ProxyID = proxyID
		}
		existing.Concurrency = 3
		existing.Priority = CRSClampPriority(src.Priority)
		existing.Status = MapCRSStatus(src.IsActive, src.Status)
		existing.Schedulable = src.Schedulable

		if err := WriteConfiguration(ctx, s.providerRepo, existing, CRSSyncConfiguration(proxyID != nil)); err != nil {
			item.Action = "failed"
			item.Error = "update failed: " + err.Error()
			result.Failed++
			result.Items = append(result.Items, item)
			continue
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	return result, nil
}

func CRSMergeMap(existing map[string]any, updates map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(updates))
	maps.Copy(out, existing)
	maps.Copy(out, updates)
	return CloneValues(out)
}

func ReconcileCRSOllamaCloudUsageExtra(
	existing *Record,
	targetPlatform, targetType string,
	targetCredentials map[string]any,
	extra map[string]any,
) {
	DiscardDeprecatedExtra(extra)
	for _, key := range []string{
		OllamaCloudUsageSessionExtraKey,
		OllamaCloudUsageAutoRefreshExtraKey,
		OllamaCloudUsageSnapshotExtraKey,
	} {
		delete(extra, key)
	}
	if existing == nil {
		return
	}
	target := &Record{Platform: targetPlatform, Type: targetType, Credentials: targetCredentials}
	if IsOllamaCloudUsageProvider(existing) && IsOllamaCloudUsageProvider(target) &&
		reflect.DeepEqual(OllamaCloudUsageIdentity(existing), OllamaCloudUsageIdentity(target)) {
		if session, ok := existing.Extra[OllamaCloudUsageSessionExtraKey]; ok {
			extra[OllamaCloudUsageSessionExtraKey] = session
		}
		if enabled, ok := existing.Extra[OllamaCloudUsageAutoRefreshExtraKey]; ok {
			extra[OllamaCloudUsageAutoRefreshExtraKey] = enabled
		}
		if snapshot, ok := existing.Extra[OllamaCloudUsageSnapshotExtraKey]; ok {
			extra[OllamaCloudUsageSnapshotExtraKey] = snapshot
		}
	}
}

func CRSDefaultName(name, id string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "CRS " + id
}

func CRSClampPriority(priority int) int {
	if priority < 1 || priority > 100 {
		return 50
	}
	return priority
}

func CRSSanitizeCredentialsMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		// Omit nil credential values.
		if v != nil {
			out[k] = v
		}
	}
	return CloneValues(out)
}

func MapCRSStatus(isActive bool, status string) string {
	if !isActive {
		return "inactive"
	}
	if strings.EqualFold(strings.TrimSpace(status), "error") {
		return "error"
	}
	return "active"
}

// CRSCleanBaseURL trims whitespace and removes the specified trailing base-URL segment.
func CRSCleanBaseURL(credentials map[string]any, suffixToRemove string) {
	if baseURL, ok := credentials["base_url"].(string); ok && baseURL != "" {
		trimmed := strings.TrimSpace(baseURL)
		if before, ok0 := strings.CutSuffix(trimmed, suffixToRemove); ok0 {
			credentials["base_url"] = before
		}
	}
}

// CRSBuildSelectedSet returns nil for all entries and an empty set for no new creations.
func CRSBuildSelectedSet(ids []string) map[string]struct{} {
	if ids == nil {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// CRSShouldCreateProvider decides creation eligibility; selection does not block updates to existing providers.
func CRSShouldCreateProvider(crsID string, selectedSet map[string]struct{}) bool {
	if selectedSet == nil {
		return true
	}
	_, ok := selectedSet[crsID]
	return ok
}

// PreviewFromCRS fetches the export, reads local source IDs once, and classifies entries in source-category order.
func (s *CRSSync) PreviewFromCRS(ctx context.Context, input SyncFromCRSInput) (*PreviewFromCRSResult, error) {
	exported, err := s.exporter.Fetch(ctx, input.BaseURL, input.Username, input.Password)
	if err != nil {
		return nil, err
	}

	// Read existing source IDs in one query.
	existingCRSIDs, err := s.providerRepo.ListCRSAccountIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing CRS providers: %w", err)
	}

	result := &PreviewFromCRSResult{
		NewProviders:      make([]CRSPreviewProvider, 0),
		ExistingProviders: make([]CRSPreviewProvider, 0),
	}

	classify := func(crsID, kind, name, platform, providerType string) {
		preview := CRSPreviewProvider{
			CRSAccountID: crsID,
			Kind:         kind,
			Name:         CRSDefaultName(name, crsID),
			Platform:     platform,
			Type:         providerType,
		}
		if _, exists := existingCRSIDs[crsID]; exists {
			result.ExistingProviders = append(result.ExistingProviders, preview)
		} else {
			result.NewProviders = append(result.NewProviders, preview)
		}
	}

	for _, src := range exported.Data.ClaudeProviders {
		authType := strings.TrimSpace(src.AuthType)
		if authType == "" {
			authType = ProviderTypeOAuth
		}
		classify(src.ID, src.Kind, src.Name, PlatformAnthropic, authType)
	}
	for _, src := range exported.Data.ClaudeConsoleProviders {
		classify(src.ID, src.Kind, src.Name, PlatformAnthropic, ProviderTypeAPIKey)
	}
	for _, src := range exported.Data.OpenAIOAuthProviders {
		classify(src.ID, src.Kind, src.Name, PlatformOpenAI, ProviderTypeOAuth)
	}
	for _, src := range exported.Data.OpenAIResponsesProviders {
		classify(src.ID, src.Kind, src.Name, PlatformOpenAI, ProviderTypeAPIKey)
	}
	for _, src := range exported.Data.GeminiOAuthProviders {
		classify(src.ID, src.Kind, src.Name, PlatformGemini, ProviderTypeOAuth)
	}
	for _, src := range exported.Data.GeminiAPIKeyProviders {
		classify(src.ID, src.Kind, src.Name, PlatformGemini, ProviderTypeAPIKey)
	}

	return result, nil
}
