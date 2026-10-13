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

// CRS source identity is global across six exported provider categories, and
// creation selection leaves updates to existing source IDs enabled. Initial
// kind and credential checks precede proxy resolution; after reaching that
// step, a saved connection can survive later provider selection or failure.
//
// Synchronization commits each source item separately and reports partial
// outcomes. Platform expiry representations and base-URL cleanup precede
// merging,
// source null credentials are omitted, and local Ollama state survives only
// unchanged supported API-key identity. Spark shadows keep their parent's
// OpenAI OAuth kind before incompatible updates are accepted. Post-import
// exchange and OpenAI shadow proxy inheritance follow provider write
// acceptance; their failures leave that item counted. Preview classifies global
// ID membership before actual import checks.

// CRSProviderStore supplies source identity lookup and provider/shadow
// configuration writes for import.
type CRSProviderStore interface {
	ShadowProxyStore // provider configuration and shadow operations
	// Create saves the assembled source provider and supplies its persisted
	// ID.
	// CRSSync counts a nil-error creation before later optional OAuth
	// refresh,
	// whose outcome is separate from the import item.
	Create(context.Context, *Record) error
	// GetByCRSAccountID finds a provider using global source identity
	// across
	// categories. A nil record permits selected creation; an existing
	// record
	// reaches update and shadow-parent checks even when creation is
	// unselected.
	GetByCRSAccountID(context.Context, string) (*Record, error)
	// ListCRSAccountIDs returns source-ID mappings used to classify preview
	// entries globally.
	ListCRSAccountIDs(context.Context) (map[string]int64, error)
}

// CRSProxyStore supplies active connection matching and new proxy persistence
// during CRS import.
type CRSProxyStore interface {
	// ListActive supplies active proxies for serial import matching and
	// cached
	// reuse.
	ListActive(context.Context) ([]egress.Proxy, error)
	// Create saves a new source proxy and supplies its stored ID. Proxy
	// creation precedes provider selection and writes, so later item skip
	// or
	// failure leaves this accepted proxy stored.
	Create(context.Context, *egress.Proxy) error
}

// CRSExporter supplies authenticated remote data for both synchronization and
// preview requests.
type CRSExporter interface {
	// Fetch authenticates to the requested CRS service and returns a nonnil
	// export on success. Endpoint, username, and password belong to this
	// query;
	// fetch errors stop sync or preview before local items are processed.
	Fetch(context.Context, string, string, string) (*transfer.CRSExportResponse, error)
}

// CRSOptions supplies synchronization timestamps and optional post-write
// refresh/diagnostic callbacks.
type CRSOptions struct {
	// Now and Warn are retained callbacks. NewCRSSync supplies the system
	// clock
	// and a no-op warning function when absent; Refresh stays optional
	// after
	// those defaults are installed.
	Now  func() time.Time     // synchronization metadata timestamp clock
	Warn func(string, ...any) // best-effort propagation diagnostic callback
	// Refresh optionally attempts OAuth maintenance after a provider write
	// succeeds; its errors are discarded and do not change the item result.
	Refresh func(context.Context, *Record) error
}

// CRSSync imports six source categories with separate per-item provider and
// proxy effects.
type CRSSync struct {
	providerRepo CRSProviderStore // provider import and shadow storage
	proxyRepo    CRSProxyStore    // active proxy matching storage
	exporter     CRSExporter      // remote CRS export query dependency
	options      CRSOptions       // normalized clock and maintenance callbacks
}

// SyncFromCRSInput carries export login and optional proxy/new-provider
// selection choices.
type SyncFromCRSInput struct {
	BaseURL     string // CRS export service endpoint
	Username    string // temporary export login username
	Password    string // temporary export login password
	SyncProxies bool   // enable imported proxy association
	// SelectedProviderIDs gates creation only. Nil permits every new source
	// ID,
	// an empty slice permits none, and existing providers still update
	// regardless of selection.
	SelectedProviderIDs []string
}

// SyncFromCRSItemResult reports one source item and its accepted, skipped, or
// failed action.
type SyncFromCRSItemResult struct {
	CRSAccountID string `json:"crs_account_id"`  // source account lookup identity
	Kind         string `json:"kind"`            // source category display label
	Name         string `json:"name"`            // source provider display name
	Action       string `json:"action"`          // created, updated, failed, or skipped
	Error        string `json:"error,omitempty"` // failed or skipped item explanation
}

// SyncFromCRSResult totals per-item outcomes and retains their source-category
// traversal order.
type SyncFromCRSResult struct {
	Created int                     `json:"created"` // accepted provider creation count
	Updated int                     `json:"updated"` // accepted provider configuration update count
	Skipped int                     `json:"skipped"` // unsupported or unselected entry count
	Failed  int                     `json:"failed"`  // failed item preparation or write count
	Items   []SyncFromCRSItemResult `json:"items"`   // results in source-category order
}

// PreviewFromCRSResult separates source entries using their presence in local
// CRS identity mappings.
type PreviewFromCRSResult struct {
	// NewProviders and ExistingProviders split the same fetched source
	// categories using global local CRS IDs. Each slice retains category
	// traversal order; preview does not apply creation selection or
	// validate
	// credentials.
	NewProviders      []CRSPreviewProvider `json:"new_providers"`      // entries with no local CRS ID
	ExistingProviders []CRSPreviewProvider `json:"existing_providers"` // entries with a local CRS ID
}

// CRSPreviewProvider describes source identity and target kind before import
// validation runs.
type CRSPreviewProvider struct {
	CRSAccountID string `json:"crs_account_id"` // source account lookup identity
	Kind         string `json:"kind"`           // source category display label
	Name         string `json:"name"`           // normalized preview display name
	Platform     string `json:"platform"`       // target provider platform identifier
	Type         string `json:"type"`           // target provider credential type
}

// NewCRSSync retains provider, proxy, and export dependencies for later serial
// synchronization. Missing clock and warning callbacks receive defaults.
// Call SyncFromCRS or PreviewFromCRS to begin work with the retained storage
// and export dependencies.
func NewCRSSync(providers CRSProviderStore, proxies CRSProxyStore, exporter CRSExporter, options CRSOptions) *CRSSync {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	return &CRSSync{providers, proxies, exporter, options}
}

// refreshOAuthToken attempts the optional maintenance callback after an import
// write has succeeded. Missing callback or returned failure leaves the item
// outcome unchanged, because import acceptance is counted independently from
// token refresh.
func (s *CRSSync) refreshOAuthToken(ctx context.Context, value *Record) {
	if s.options.Refresh != nil {
		// The provider write has already succeeded before this
		// maintenance
		// callback; discard refresh failure so the item still reports
		// its
		// accepted create/update result.
		_ = s.options.Refresh(ctx, value)
	}
}

// GuardCRSShadowParentInvariant rejects conversion of a provider with Spark
// shadows away from OpenAI OAuth. Nil existing records and OpenAI OAuth targets
// succeed immediately; other targets require a successful shadow lookup.
//
// CRS source IDs are global across categories, so one source can select a
// parent created under another platform/type. Both target dimensions are
// checked before update to keep shared shadow credentials resolvable. A lookup
// error or present shadow returns an item failure; callers remove shadows
// before an incompatible conversion.
func GuardCRSShadowParentInvariant(ctx context.Context, repo ShadowProxyStore, existing *Record, newPlatform, newType string) error {
	if existing == nil {
		return nil
	}
	// An OpenAI OAuth target remains a valid shadow parent, so no shadow
	// lookup is needed.
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

// SyncFromCRS fetches six source categories and returns ordered per-item
// outcomes with partial success. Export errors stop the operation; later
// preparation, lookup, proxy, shadow, or provider-write failures affect their
// item while remaining entries continue. Selection limits new creation and
// permits every existing-row update.
//
// Items passing the initial credential checks resolve source proxies before
// creation selection and provider persistence. An accepted proxy can remain
// after a later lookup, shadow check, or provider write fails.
// Credentials/Extra merge supported stored containers, omit source null
// credential values, and restore managed Ollama state only for unchanged
// supported identity. A nil imported proxy leaves the existing association
// outside CRSSyncConfiguration's selected fields.
//
// OAuth post-import refresh runs after accepted writes and leaves their counts
// unchanged. OpenAI OAuth parent updates also attempt shadow proxy inheritance
// after configuration acceptance, logging that failure separately. Counts
// describe accepted repository calls, not an atomic transaction over the
// export; earlier item effects remain when later items fail or their
// context-dependent operations are rejected.
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
		// A failed active-proxy listing does not stop provider import;
		// matching
		// uses whatever slice was returned and can create missing
		// source
		// connections.
		proxies, _ = s.proxyRepo.ListActive(ctx)
	}

	// Claude sources accept OAuth or setup-token credentials with a
	// nonblank
	// access token. Each item resolves its proxy before source-ID lookup,
	// merges saved metadata, and restricts selection to creation; OAuth
	// refresh
	// follows accepted writes, while setup-token items skip that exchange.
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
		// Claude endpoint consumers use their root path; remove an
		// imported /v1
		// suffix when matched, leaving unmatched base URL text for its
		// ordinary
		// consumer rules.
		CRSCleanBaseURL(credentials, "/v1")
		// Claude credential readers accept Unix seconds; normalize
		// parseable
		// RFC3339 input while retaining an unparseable source value for
		// later
		// credential interpretation.
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

		// Copy source Extra fields before adding synchronization
		// metadata.
		extra := make(map[string]any)
		if src.Extra != nil {
			maps.Copy(extra, src.Extra)
		}
		extra["crs_account_id"] = src.ID
		extra["crs_kind"] = src.Kind
		extra["crs_synced_at"] = now
		// Copy organization and account IDs from source credentials to
		// Extra.
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
			// Use the accepted persisted row for optional OAuth
			// exchange;
			// failure in that later maintenance step leaves the
			// import item
			// outcome successful.
			if targetType == ProviderTypeOAuth {
				s.refreshOAuthToken(ctx, provider)
			}
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// Source IDs can reuse a parent created as OpenAI OAuth. Check
		// its
		// Spark shadows before accepting this incompatible category
		// conversion.
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

		// Use the accepted persisted row for optional OAuth exchange;
		// failure
		// in that later maintenance step leaves the import item outcome
		// successful.
		if targetType == ProviderTypeOAuth {
			s.refreshOAuthToken(ctx, existing)
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// Console sources become Anthropic API Key records and can supply their
	// concurrency limit. Their Extra starts from synchronization metadata,
	// then
	// inherits existing values; proxy and provider writes remain separate,
	// and
	// shadow-parent conversion is checked before configuration replacement.
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

		// Source IDs can reuse a parent created as OpenAI OAuth. Check
		// its
		// Spark shadows before accepting this incompatible category
		// conversion.
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

	// OpenAI OAuth sources replace accepted source configuration before
	// post-import refresh and separate shadow proxy propagation. Source
	// metadata is merged with saved values and deprecated keys are dropped;
	// an
	// existing proxy is retained when matching supplies no imported
	// association.
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
		// OpenAI imported expiry uses Unix seconds after successful
		// parsing; a
		// malformed source string remains present for the post-import
		// credential refresh path.
		if expiresAtStr, ok := credentials["expires_at"].(string); ok && expiresAtStr != "" {
			if t, err := time.Parse(time.RFC3339, expiresAtStr); err == nil {
				credentials["expires_at"] = t.Unix()
			}
		}
		priority := CRSClampPriority(src.Priority)
		concurrency := 3
		status := MapCRSStatus(src.IsActive, src.Status)

		// Copy source Extra fields before adding synchronization
		// metadata.
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
		// Remove obsolete billing-probe and long-context settings from
		// merged
		// Extra before saving this source configuration, including keys
		// inherited
		// from an older local record.
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
			// Creation has already saved this OpenAI provider. A
			// failed optional
			// token exchange leaves that created row available for
			// later refresh
			// and keeps this item counted.
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

		// Configuration has accepted the imported OpenAI identity
		// before this
		// exchange. Refresh failure keeps the accepted update while
		// shadow proxy
		// propagation remains a separate following step.
		s.refreshOAuthToken(ctx, existing)

		// The parent configuration write has returned success before
		// shadow
		// proxy inheritance runs. Report propagation failure separately
		// so it
		// cannot reverse the accepted parent item outcome.
		if perr := PropagateProviderProxyToShadows(ctx, s.providerRepo, existing.ID, existing.ProxyID); perr != nil {
			s.options.Warn("crs_sync_propagate_proxy_to_shadows_failed", "provider_id", existing.ID, "error", perr)
		}

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// Responses sources become OpenAI API Key records after source base-URL
	// defaulting and suffix cleanup. New-creation selection follows proxy
	// resolution, and existing OAuth parents with Spark shadows are
	// rejected
	// before their credential type can be replaced.
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
		// The Responses source map is normalized before sanitizing its
		// copy;
		// removing a matched /v1 suffix supplies the root endpoint
		// expected by
		// OpenAI consumers.
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

		// This API Key conversion would invalidate an OpenAI OAuth
		// shadow parent.
		// Require shadows to be deleted before changing their parent's
		// credential type.
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

	// Gemini OAuth sources require refresh_token and represent parsed
	// expiry as
	// Unix-seconds text. Proxy resolution and source-ID merge precede
	// selected
	// creation or guarded configuration update; accepted rows can still
	// lack
	// access_token until their post-import exchange completes.
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
		// Gemini credential builders use decimal Unix-seconds text;
		// convert
		// parseable RFC3339 expiry while leaving malformed source text
		// available to later credential handling.
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
			// Gemini creation accepts refresh_token before
			// access-token exchange.
			// The saved row stays counted even when this follow-up
			// cannot supply
			// its first access token.
			s.refreshOAuthToken(ctx, provider)
			item.Action = "created"
			result.Created++
			result.Items = append(result.Items, item)
			continue
		}

		// Source IDs can reuse a parent created as OpenAI OAuth. Check
		// its
		// Spark shadows before accepting this incompatible category
		// conversion.
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

		// Gemini configuration is already stored before renewal. A
		// failed
		// exchange leaves that accepted identity for later maintenance
		// while
		// this item still reports an update.
		s.refreshOAuthToken(ctx, existing)

		item.Action = "updated"
		result.Updated++
		result.Items = append(result.Items, item)
	}

	// Gemini API Key sources select the official endpoint when missing and
	// merge metadata with existing Extra. Proxy creation is independent,
	// selection skips only new rows, and shadow-parent conversion is
	// checked
	// before an existing record is changed.
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

		// Source IDs can reuse a parent created as OpenAI OAuth. Check
		// its
		// Spark shadows before accepting this incompatible category
		// conversion.
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

// CRSMergeMap returns a map with update keys overriding existing keys, then
// copies supported containers through CloneValues. Present nil updates retain
// precedence; unsupported mutable values can remain borrowed, so callers
// coordinate them while sharing the result.
func CRSMergeMap(existing map[string]any, updates map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(updates))
	maps.Copy(out, existing)
	maps.Copy(out, updates)
	return CloneValues(out)
}

// ReconcileCRSOllamaCloudUsageExtra mutates the supplied Extra map to remove
// deprecated and imported managed usage fields. New providers retain no
// imported session state. Existing local values return after both provider
// kind/base-URL checks and equality of their generated identity maps.
//
// A usable identity compares the fixed host and exact API key. Two missing
// identity maps also compare equal and restore local state; callers provide
// usable API keys when binding that state to a real group. Rotating a usable
// key or leaving a supported provider kind/base URL clears those fields.
// Restored session, refresh flag, and snapshot values are assigned directly
// from existing Extra. Callers provide a writable map and copy supported nested
// containers later if they need independent ownership.
func ReconcileCRSOllamaCloudUsageExtra(
	existing *Record,
	targetPlatform, targetType string,
	targetCredentials map[string]any,
	extra map[string]any,
) {
	// Imported managed values have not been tied to this local provider
	// identity. Remove their keys first, then restore local state only
	// after
	// comparing the existing and target host/API-key identities.
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
	// Kind/base-URL eligibility does not require an API-key string. This
	// DeepEqual therefore also accepts two missing identity maps, while
	// real
	// group membership requires a usable key.
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

// CRSDefaultName returns trimmed source name when nonblank, otherwise a CRS
// label followed by the supplied ID. The ID is used unchanged, so callers
// decide its display spelling before using that fallback.
func CRSDefaultName(name, id string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "CRS " + id
}

// CRSClampPriority returns a source priority from one through one hundred
// unchanged. Out-of-range values select fifty so imported records receive
// the importer's supported priority range before configuration is written.
func CRSClampPriority(priority int) int {
	if priority < 1 || priority > 100 {
		// Zero also represents an omitted integer source priority.
		return 50
	}
	return priority
}

// CRSSanitizeCredentialsMap removes nil-interface entries and copies supported
// remaining containers through CloneValues. Nil input returns an empty map.
// Typed nil values remain supplied entries; encodable ones can serialize as
// JSON null. Unsupported mutable values stay borrowed, while omitted keys
// retain their existing values in CRSMergeMap.
func CRSSanitizeCredentialsMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		// JSON-decoded null entries are nil interfaces and are omitted
		// here,
		// retaining stored keys during merge. A typed nil still
		// supplies its key
		// to that later merge.
		if v != nil {
			out[k] = v
		}
	}
	return CloneValues(out)
}

// MapCRSStatus returns inactive when the source active flag is false, otherwise
// error for trimmed case-insensitive error text. Other active-source status
// values become active; callers use that normalized value in configuration
// replacement.
func MapCRSStatus(isActive bool, status string) string {
	if !isActive {
		return "inactive"
	}
	if strings.EqualFold(strings.TrimSpace(status), "error") {
		return "error"
	}
	return "active"
}

// CRSCleanBaseURL updates a nonempty string base_url only when its trimmed text
// has the requested suffix, removing that suffix. Missing keys, non-string
// values, and unmatched suffixes leave the stored value unchanged, including
// whitespace.
func CRSCleanBaseURL(credentials map[string]any, suffixToRemove string) {
	// Normalize matched endpoint suffixes for platform root consumers;
	// keeping
	// unmatched stored text leaves its interpretation with those consumers.
	if baseURL, ok := credentials["base_url"].(string); ok && baseURL != "" {
		trimmed := strings.TrimSpace(baseURL)
		if before, ok0 := strings.CutSuffix(trimmed, suffixToRemove); ok0 {
			credentials["base_url"] = before
		}
	}
}

// CRSBuildSelectedSet returns nil for absent selection and a deduplicated
// membership map for a supplied slice. Explicit empty selection returns a
// nonnil empty set; source IDs are used exactly, including whitespace or empty
// strings.
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

// CRSShouldCreateProvider reports whether an exact source ID is admitted for
// new provider creation. Nil selection allows every ID, while an empty map
// allows none; existing provider updates bypass this choice in SyncFromCRS.
func CRSShouldCreateProvider(crsID string, selectedSet map[string]struct{}) bool {
	if selectedSet == nil {
		return true
	}
	_, ok := selectedSet[crsID]
	return ok
}

// PreviewFromCRS fetches remote entries, reads all local source IDs once, and
// classifies new/existing entries in source-category order. Fetch or local-ID
// lookup failure returns an error before a preview is returned.
//
// Preview shares global source-ID membership with synchronization and defaults
// blank Claude auth type to OAuth, but it skips credential, selection, proxy,
// and shadow conversion validation. Unsupported auth types can appear here and
// later be skipped during import. The result contains display identity and
// target kind, leaving credentials in the fetched export.
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
