package provider

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// archive.go exports provider and proxy records and imports them with
// transfer-key resolution and per-entry results.
// Provider backups identify proxies by transfer keys rather than local database
// IDs. Export builds those keys from the selected proxy records and attaches
// matching keys to provider entries; import resolves them to local IDs before
// creation. Missing proxy matches fail individual provider entries after proxy
// import has already completed.
//
// Credential shadows are excluded because the archive format cannot represent
// their parent relationship. Export copies the supported provider containers
// used by the payload, while import clones each submitted entry before identity
// enrichment and normalization. Providers are created sequentially with partial
// results, and Antigravity privacy work is registered after the import loop.
// Creation counts are collected separately from those asynchronous observations;
// the supplied services retain their own transaction completion rules.

// ArchiveProviders supplies provider selection and creation for backup transfer
// operations.
type ArchiveProviders interface {
	// ListProviders returns one filtered and sorted provider page with its
	// matching total.
	ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]Record, int64, error)
	// GetProvidersByIDs reads the provider records selected by local identifiers.
	GetProvidersByIDs(context.Context, []int64) ([]*Record, error)
	// CreateProvider validates and saves one nonnil imported configuration. It
	// can normalize the input's maps; the returned provider supplies identity and
	// settings for optional probing and privacy work.
	CreateProvider(context.Context, *CreateProviderInput) (*Record, error)
}

// ArchiveProxies transfers proxy identities before provider entries are bound
// during import.
type ArchiveProxies interface {
	// GetProxiesByIDs reads selected proxy records for stable transfer-key
	// construction.
	GetProxiesByIDs(context.Context, []int64) ([]egress.Proxy, error)
	// ImportForProviderBinding creates or reuses transfer proxies and maps their
	// keys to local IDs. Partial proxy outcomes accompany errors so Archive can
	// retain completed results before provider creation starts.
	ImportForProviderBinding(context.Context, []egress.TransferProxy) (map[string]int64, egress.ProxyImportResult, error)
}

// ArchiveExportQuery selects explicit providers or filtered pages for backup
// export.
type ArchiveExportQuery struct {
	// IDs selects explicit providers when nonempty. Empty input uses the query
	// filters and reads all matching pages before payload construction.
	IDs       []int64
	Platform, // upstream platform filter
	Type, // provider authentication filter
	Status, // persisted status filter
	Search, // provider text search
	PrivacyMode, // stored privacy mode filter
	SortBy, // provider sort field
	SortOrder string // provider sort direction
	GroupID int64 // routing group membership filter
	// IncludeProxies is called after reading providers and removing shadows. Nil
	// enables proxy export; callback errors are wrapped for HTTP input-error
	// classification.
	IncludeProxies func() (bool, error)
}

// ArchiveInputError identifies deferred archive query validation failures for
// HTTP callers.
type ArchiveInputError struct {
	// Err is the deferred query-validation cause. Export supplies a nonnil error,
	// which Error dereferences and Unwrap returns to inspection callers.
	Err error
}

// ArchiveOptions binds archive clocks, identity hints, and post-import service
// callbacks.
type ArchiveOptions struct {
	Now func() time.Time // UTC export timestamp clock
	// DecodeIDToken provides nonauthentication identity hints from OpenAI ID
	// tokens. Nil skips enrichment, and decode errors leave imported credentials
	// as supplied.
	DecodeIDToken func(string) (*ArchiveIdentityHints, error)
	// Probe receives routing metadata after each successful provider creation. Nil
	// skips post-import probing; completed creation is counted after this callback
	// returns.
	Probe func(ProviderSnapshot)
	// ForcePrivacy handles copied Antigravity OAuth records inside the background
	// task. It is called only when both it and Background are configured.
	ForcePrivacy func(context.Context, *Record) string
	// Background registers post-import privacy work after the provider loop. A
	// missing callback skips scheduling, and Archive ignores its returned
	// admission boolean.
	Background func(string, func()) bool
	Info,      // completed import information logging
	Error, // background privacy panic logging
	Debug func(string, ...any) // optional identity enrichment diagnostics
}

// Archive coordinates provider backups with proxy transfer and post-import
// operations.
type Archive struct {
	providers ArchiveProviders // provider selection and creation
	proxies   ArchiveProxies   // proxy transfer and lookup
	options   ArchiveOptions   // retained archive service callbacks
}

// Error returns the wrapped archive input error's message for HTTP reporting.
func (e *ArchiveInputError) Error() string { return e.Err.Error() }

// Unwrap exposes the archive input error's cause to standard error inspection.
func (e *ArchiveInputError) Unwrap() error { return e.Err }

// NewArchive binds provider and proxy transfer services plus post-import
// callbacks. A missing clock uses time.Now, missing log callbacks become
// no-ops, and optional identity or post-import callbacks are retained when
// supplied.
func NewArchive(providers ArchiveProviders, proxies ArchiveProxies, options ArchiveOptions) *Archive {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Info == nil {
		options.Info = func(string, ...any) {}
	}
	if options.Error == nil {
		options.Error = func(string, ...any) {}
	}
	if options.Debug == nil {
		options.Debug = func(string, ...any) {}
	}
	return &Archive{providers: providers, proxies: proxies, options: options}
}

// Export builds a provider backup from explicit IDs or filtered pages. It
// excludes credential shadows before invoking IncludeProxies, defaults proxy
// inclusion to true, and wraps callback validation errors as ArchiveInputError.
//
// Provider entries reference transfer proxy keys that can be resolved across
// installations. The payload carries stored credentials and settings, copying
// the supported containers handled by CloneRecord before returning them.
// Missing selected proxy records leave the corresponding provider proxy key
// unset; export errors stop payload construction.
func (h *Archive) Export(ctx context.Context, query ArchiveExportQuery) (transfer.DataPayload, error) {
	providers, err := h.resolveExportProviders(ctx, query)
	if err != nil {
		return transfer.DataPayload{}, err
	}

	// The export format cannot represent parent links and import requires
	// credentials, so exclude Spark shadows. Their priority, concurrency,
	// groups, and status must be configured again after recreating the shadows.
	// The UI displays the excluded count from skipped_shadows.
	skippedShadows := 0
	exportable := make([]Record, 0, len(providers))
	for i := range providers {
		if providers[i].IsCredentialShadow() {
			skippedShadows++
			continue
		}
		exportable = append(exportable, providers[i])
	}
	providers = exportable
	if skippedShadows > 0 {
		h.options.Info("export_skipped_spark_shadows", "count", skippedShadows)
	}

	includeProxies := true
	if query.IncludeProxies != nil {
		includeProxies, err = query.IncludeProxies()
	}
	if err != nil {
		return transfer.DataPayload{}, &ArchiveInputError{Err: err}
	}

	var proxies []egress.Proxy
	if includeProxies {
		proxies, err = h.resolveExportProxies(ctx, providers)
		if err != nil {
			return transfer.DataPayload{}, err
		}
	} else {
		proxies = []egress.Proxy{}
	}

	// The transfer format names backup proxies. Resolve names from the selected
	// records before serializing so exported references do not carry local
	// database IDs.
	proxyNameByID := make(map[int64]string, len(proxies))
	for i := range proxies {
		proxyNameByID[proxies[i].ID] = proxies[i].Name
	}

	proxyKeyByID := make(map[int64]string, len(proxies))
	dataProxies := make([]transfer.DataProxy, 0, len(proxies))
	for i := range proxies {
		p := proxies[i]
		// Transfer keys attach provider selections to proxies across
		// installations; local IDs are used only while assembling this
		// installation's backup payload.
		key := egress.BuildTransferProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)
		proxyKeyByID[p.ID] = key

		var expiresAt *int64
		if p.ExpiresAt != nil {
			// Store expiration as Unix seconds for the transfer representation.
			v := p.ExpiresAt.Unix()
			expiresAt = &v
		}
		var backupProxyName string
		if p.BackupProxyID != nil {
			backupProxyName = proxyNameByID[*p.BackupProxyID]
		}
		dataProxies = append(dataProxies, transfer.DataProxy{
			ProxyKey:        key,
			Name:            p.Name,
			Protocol:        p.Protocol,
			Host:            p.Host,
			Port:            p.Port,
			Username:        p.Username,
			Password:        p.Password,
			Status:          p.Status,
			ExpiresAt:       expiresAt,
			FallbackMode:    p.FallbackMode,
			BackupProxyName: backupProxyName,
			ExpiryWarnDays:  p.ExpiryWarnDays,
		})
	}

	dataProviders := make([]transfer.DataProvider, 0, len(providers))
	for i := range providers {
		// Copy supported mutable provider containers before returning
		// credentials in the payload so import/export callers cannot mutate
		// those stored JSON containers.
		acc := *CloneRecord(&providers[i])
		var proxyKey *string
		if acc.ProxyID != nil {
			if key, ok := proxyKeyByID[*acc.ProxyID]; ok {
				proxyKey = &key
			}
		}
		var expiresAt *int64
		if acc.ExpiresAt != nil {
			// Store provider expiration as Unix seconds for archive creation input.
			v := acc.ExpiresAt.Unix()
			expiresAt = &v
		}
		dataProviders = append(dataProviders, transfer.DataProvider{
			Name:               acc.Name,
			Notes:              acc.Notes,
			Platform:           acc.Platform,
			Type:               acc.Type,
			Credentials:        acc.Credentials,
			Extra:              acc.Extra,
			ProxyKey:           proxyKey,
			Concurrency:        archiveIntPointer(acc.Concurrency),
			Priority:           archiveIntPointer(acc.Priority),
			RateMultiplier:     acc.RateMultiplier,
			ExpiresAt:          expiresAt,
			AutoPauseOnExpired: &acc.AutoPauseOnExpired,
		})
	}

	payload := transfer.DataPayload{
		Type:           transfer.DataType,
		Version:        transfer.DataVersion,
		ExportedAt:     h.options.Now().UTC().Format(time.RFC3339),
		Proxies:        dataProxies,
		Providers:      dataProviders,
		SkippedShadows: skippedShadows,
	}

	return payload, nil
}

// Import processes proxies first, then creates cloned provider entries
// sequentially with no group bindings. Provider validation, missing proxy keys,
// and creation errors are recorded per entry while later entries continue. A
// proxy-import error returns the proxy results collected so far.
//
// OpenAI ID tokens can enrich missing identity hints before creation, using the
// configured nonauthentication decoder. Successful entries trigger the optional
// probe callback, and copied Antigravity OAuth records are collected for a
// later background privacy task. Archive does not compensate completed creation
// calls when later entries or privacy fail; durability follows the supplied
// services' transaction ownership.
func (h *Archive) Import(ctx context.Context, req transfer.DataImportRequest) (transfer.DataImportResult, error) {
	dataPayload := req.Data
	result := transfer.DataImportResult{}

	// Proxy creation and reuse precede provider writes so every transferred
	// proxy key can be resolved locally; these completed proxy results survive
	// later entry failures.
	proxyKeyToID, imported, err := h.proxies.ImportForProviderBinding(ctx, dataPayload.Proxies)
	result.ProxyCreated = imported.ProxyCreated
	result.ProxyReused = imported.ProxyReused
	result.ProxyFailed = imported.ProxyFailed
	result.Errors = imported.Errors
	if err != nil {
		return result, err
	}

	var privacyProviders []*Record

	for i := range dataPayload.Providers {
		// Identity enrichment and creation normalize this entry's credentials.
		// Clone supported JSON containers first so those changes stay separate
		// from the import request.
		item := cloneArchiveItem(dataPayload.Providers[i])
		if err := ValidateArchiveProvider(item); err != nil {
			result.ProviderFailed++
			result.Errors = append(result.Errors, transfer.DataImportError{
				Kind:    "provider",
				Name:    item.Name,
				Message: err.Error(),
			})
			continue
		}

		var proxyID *int64
		if item.ProxyKey != nil && *item.ProxyKey != "" {
			if id, ok := proxyKeyToID[*item.ProxyKey]; ok {
				proxyID = &id
			} else {
				result.ProviderFailed++
				result.Errors = append(result.Errors, transfer.DataImportError{
					Kind:     "provider",
					Name:     item.Name,
					ProxyKey: *item.ProxyKey,
					Message:  "proxy_key not found",
				})
				continue
			}
		}

		h.enrichIdentity(&item)

		providerInput := &CreateProviderInput{
			Name:               item.Name,
			Notes:              item.Notes,
			Platform:           item.Platform,
			Type:               item.Type,
			Credentials:        item.Credentials,
			Extra:              item.Extra,
			ProxyID:            proxyID,
			Concurrency:        archiveIntValue(item.Concurrency),
			Priority:           archiveIntValue(item.Priority),
			RateMultiplier:     item.RateMultiplier,
			GroupIDs:           nil,
			ExpiresAt:          item.ExpiresAt,
			AutoPauseOnExpired: item.AutoPauseOnExpired,
		}

		created, err := h.providers.CreateProvider(ctx, providerInput)
		if err != nil {
			result.ProviderFailed++
			result.Errors = append(result.Errors, transfer.DataImportError{
				Kind:    "provider",
				Name:    item.Name,
				Message: err.Error(),
			})
			continue
		}
		// Collect OAuth copies for privacy after the full import loop. Count each
		// completed creation separately so later privacy observations do not
		// redefine these per-entry results.
		if created.Platform == PlatformAntigravity && created.Type == ProviderTypeOAuth {
			privacyProviders = append(privacyProviders, CloneRecord(created))
		}
		if h.options.Probe != nil {
			h.options.Probe(created.RoutingSnapshot())
		}
		result.ProviderCreated++
	}

	// Register privacy work after completed imports, using copies retained
	// above. Background admission and privacy outcomes do not change the
	// returned creation counts.
	if len(privacyProviders) > 0 && h.options.Background != nil && h.options.ForcePrivacy != nil {
		h.options.Background("handler/admin/provider_data.go:importData", func() {
			defer func() {
				// Provider creation calls have returned before privacy runs. Log a
				// panic and terminate this task; stored changes still follow their
				// creation services' transaction ownership and recorded results.
				if r := recover(); r != nil {
					h.options.Error("import_antigravity_privacy_panic", "recover", r)
				}
			}()
			bgCtx := context.Background()
			for _, acc := range privacyProviders {
				h.options.ForcePrivacy(bgCtx, acc)
			}
			h.options.Info("import_antigravity_privacy_done", "count", len(privacyProviders))
		})
	}

	return result, nil
}

// listProvidersFiltered collects every matching provider page using the
// requested filters and sorting. It stops at the reported total or an empty
// page, and a read error discards the collected result.
func (h *Archive) listProvidersFiltered(ctx context.Context, platform, providerType, status, search string, groupID int64, privacyMode, sortBy, sortOrder string) ([]Record, error) {
	page := 1
	// Bound each export query while collecting all matching provider pages.
	pageSize := 1000
	var out []Record
	for {
		items, total, err := h.providers.ListProviders(ctx, page, pageSize, platform, providerType, status, search, groupID, privacyMode, sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(out) >= int(total) || len(items) == 0 {
			break
		}
		page++
	}
	return out, nil
}

// resolveExportProviders uses explicit IDs when supplied and otherwise reads
// filtered pages. Nil ID-lookup records are skipped, values are copied here,
// and Export later copies the supported mutable containers handled by
// CloneRecord.
func (h *Archive) resolveExportProviders(ctx context.Context, query ArchiveExportQuery) ([]Record, error) {
	if len(query.IDs) > 0 {
		providers, err := h.providers.GetProvidersByIDs(ctx, query.IDs)
		if err != nil {
			return nil, err
		}
		out := make([]Record, 0, len(providers))
		for _, acc := range providers {
			if acc == nil {
				continue
			}
			out = append(out, *acc)
		}
		return out, nil
	}

	return h.listProvidersFiltered(ctx, query.Platform, query.Type, query.Status, query.Search, query.GroupID, query.PrivacyMode, query.SortBy, query.SortOrder)
}

// resolveExportProxies reads each positive proxy ID referenced by the selected
// providers once. Missing or nonpositive selections are ignored, and an
// allocated empty slice is returned when there are no proxy IDs.
func (h *Archive) resolveExportProxies(ctx context.Context, providers []Record) ([]egress.Proxy, error) {
	if len(providers) == 0 {
		return []egress.Proxy{}, nil
	}

	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for i := range providers {
		if providers[i].ProxyID == nil {
			continue
		}
		id := *providers[i].ProxyID
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []egress.Proxy{}, nil
	}

	return h.proxies.GetProxiesByIDs(ctx, ids)
}

// enrichIdentity decodes an available OpenAI OAuth ID token to fill missing
// import hints. A missing decoder or token skips enrichment; decoder failures
// are logged and leave the entry available for creation.
func (h *Archive) enrichIdentity(item *transfer.DataProvider) {
	token := ArchiveIDToken(item)
	if token == "" || h.options.DecodeIDToken == nil {
		return
	}
	hints, err := h.options.DecodeIDToken(token)
	if err != nil {
		// Identity hints are supplemental to saved credentials. A decoding
		// failure leaves creation eligible to validate the entry rather than
		// aborting the whole import.
		h.options.Debug("import_enrich_id_token_decode_failed", "provider", item.Name, "error", err)
		return
	}
	FillArchiveIdentity(item, hints)
}

// cloneArchiveItem copies the scalar pointers and supported mutable containers
// in one import entry. Unrecognized reference-valued objects in the maps remain
// shared under CloneValues, so callers use the archive's JSON representations.
func cloneArchiveItem(value transfer.DataProvider) transfer.DataProvider {
	value.Credentials = CloneValues(value.Credentials)
	value.Extra = CloneValues(value.Extra)
	value.Notes = clonePointer(value.Notes)
	value.ProxyKey = clonePointer(value.ProxyKey)
	value.Concurrency = clonePointer(value.Concurrency)
	value.Priority = clonePointer(value.Priority)
	value.RateMultiplier = clonePointer(value.RateMultiplier)
	value.ExpiresAt = clonePointer(value.ExpiresAt)
	value.AutoPauseOnExpired = clonePointer(value.AutoPauseOnExpired)
	return value
}

// archiveIntPointer returns a separate pointer containing one archive integer
// value.
func archiveIntPointer(value int) *int { return &value }

// archiveIntValue dereferences a supplied archive integer or returns zero for
// an absent pointer. Provider creation then applies its own concurrency default
// or retains the priority from that resulting value.
func archiveIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// ListProvidersForImport collects all filtered provider pages for import
// identity matching. It uses the same page and stopping rules as export
// selection, returning any storage read error to the importer.
func (h *Archive) ListProvidersForImport(ctx context.Context, platform, kind, status, search string, groupID int64, privacy, sortBy, sortOrder string) ([]Record, error) {
	return h.listProvidersFiltered(ctx, platform, kind, status, search, groupID, privacy, sortBy, sortOrder)
}
