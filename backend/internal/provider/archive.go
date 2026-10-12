package provider

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// ArchiveProviders supplies provider management operations for file import and export.
type ArchiveProviders interface {
	ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]Record, int64, error)
	GetProvidersByIDs(context.Context, []int64) ([]*Record, error)
	CreateProvider(context.Context, *CreateProviderInput) (*Record, error)
}
type ArchiveProxies interface {
	GetProxiesByIDs(context.Context, []int64) ([]egress.Proxy, error)
	ImportForProviderBinding(context.Context, []egress.TransferProxy) (map[string]int64, egress.ProxyImportResult, error)
}
type ArchiveExportQuery struct {
	IDs                                                            []int64
	Platform, Type, Status, Search, PrivacyMode, SortBy, SortOrder string
	GroupID                                                        int64
	// Validate include_proxies after reading providers and excluding shadows.
	IncludeProxies func() (bool, error)
}
type ArchiveInputError struct{ Err error }

type ArchiveOptions struct {
	Now                func() time.Time
	DecodeIDToken      func(string) (*ArchiveIdentityHints, error)
	Probe              func(ProviderSnapshot)
	ForcePrivacy       func(context.Context, *Record) string
	Background         func(string, func()) bool
	Info, Error, Debug func(string, ...any)
}

// Archive exports provider backups, imports entries, and runs post-import actions through injected services.
type Archive struct {
	providers ArchiveProviders
	proxies   ArchiveProxies
	options   ArchiveOptions
}

func (e *ArchiveInputError) Error() string { return e.Err.Error() }
func (e *ArchiveInputError) Unwrap() error { return e.Err }

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

func (h *Archive) Export(ctx context.Context, query ArchiveExportQuery) (transfer.DataPayload, error) {
	providers, err := h.resolveExportProviders(ctx, query)
	if err != nil {
		return transfer.DataPayload{}, err
	}

	// The export format cannot represent parent links and import requires credentials, so exclude Spark shadows.
	// Their priority, concurrency, groups, and status must be configured again after recreating the shadows.
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

	// Resolve backup proxy names from their IDs for export.
	proxyNameByID := make(map[int64]string, len(proxies))
	for i := range proxies {
		proxyNameByID[proxies[i].ID] = proxies[i].Name
	}

	proxyKeyByID := make(map[int64]string, len(proxies))
	dataProxies := make([]transfer.DataProxy, 0, len(proxies))
	for i := range proxies {
		p := proxies[i]
		key := egress.BuildTransferProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)
		proxyKeyByID[p.ID] = key

		var expiresAt *int64
		if p.ExpiresAt != nil {
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
		acc := *CloneRecord(&providers[i])
		var proxyKey *string
		if acc.ProxyID != nil {
			if key, ok := proxyKeyByID[*acc.ProxyID]; ok {
				proxyKey = &key
			}
		}
		var expiresAt *int64
		if acc.ExpiresAt != nil {
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

func (h *Archive) Import(ctx context.Context, req transfer.DataImportRequest) (transfer.DataImportResult, error) {
	dataPayload := req.Data
	result := transfer.DataImportResult{}

	proxyKeyToID, imported, err := h.proxies.ImportForProviderBinding(ctx, dataPayload.Proxies)
	result.ProxyCreated = imported.ProxyCreated
	result.ProxyReused = imported.ProxyReused
	result.ProxyFailed = imported.ProxyFailed
	result.Errors = imported.Errors
	if err != nil {
		return result, err
	}

	// Collect Antigravity OAuth providers for asynchronous privacy setup.
	var privacyProviders []*Record

	for i := range dataPayload.Providers {
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
		// Defer privacy setup until all entries have been imported.
		if created.Platform == PlatformAntigravity && created.Type == ProviderTypeOAuth {
			privacyProviders = append(privacyProviders, CloneRecord(created))
		}
		if h.options.Probe != nil {
			h.options.Probe(created.RoutingSnapshot())
		}
		result.ProviderCreated++
	}

	// Schedule privacy setup asynchronously so the import response does not wait for it.
	if len(privacyProviders) > 0 && h.options.Background != nil && h.options.ForcePrivacy != nil {
		h.options.Background("handler/admin/provider_data.go:importData", func() {
			defer func() {
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

func (h *Archive) listProvidersFiltered(ctx context.Context, platform, providerType, status, search string, groupID int64, privacyMode, sortBy, sortOrder string) ([]Record, error) {
	page := 1
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

func (h *Archive) enrichIdentity(item *transfer.DataProvider) {
	token := ArchiveIDToken(item)
	if token == "" || h.options.DecodeIDToken == nil {
		return
	}
	hints, err := h.options.DecodeIDToken(token)
	if err != nil {
		h.options.Debug("import_enrich_id_token_decode_failed", "provider", item.Name, "error", err)
		return
	}
	FillArchiveIdentity(item, hints)
}

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
func archiveIntPointer(value int) *int { return &value }
func archiveIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// ListProvidersForImport reads every matching page for file-import matching.
func (h *Archive) ListProvidersForImport(ctx context.Context, platform, kind, status, search string, groupID int64, privacy, sortBy, sortOrder string) ([]Record, error) {
	return h.listProvidersFiltered(ctx, platform, kind, status, search, groupID, privacy, sortBy, sortOrder)
}
