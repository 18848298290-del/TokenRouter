package egress

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// proxy_transfer.go exports proxy backups and imports connection records with
// destination backup references.
// Proxy backups match connection identities across instances and use names for
// backup references that local IDs cannot preserve. Export resolves names
// within its selected set, while import maps existing records and earlier batch
// creations before each later item is processed.
//
// Items can be created, reused, or rejected independently, and successful
// writes remain when another item fails. Status synchronization carries a full
// expiration/fallback group because ProxyAdmin replaces those values even for
// nil or zero input. Provider-binding imports reread reused records and
// suppress status-update errors; ordinary proxy imports report those errors and
// optionally schedule probes after the batch. Task admission and probe errors
// are best effort, with background lifetimes owned by the application tracker.

// ProxyExportQuery selects backup records by IDs or filtered paginated
// administration queries.
type ProxyExportQuery struct {
	// IDs takes precedence over Protocol, Status, Search, SortBy, and SortOrder.
	// Without selected IDs, Export reads every filtered page using the supplied
	// administration ordering.
	IDs []int64 // selected proxy database identities
	// Protocol filters proxy scheme; Status filters stored availability; Search
	// filters proxy names; SortBy selects ordering; SortOrder selects direction.
	Protocol, Status, Search, SortBy, SortOrder string
}

// ProxyImportResult summarizes created, reused, and rejected items with their
// partial import diagnostics.
type ProxyImportResult struct {
	// ProxyCreated counts stored new items; ProxyReused counts matching existing
	// items; ProxyFailed counts validation or creation failures. Status-update
	// diagnostics can accompany created or reused counts without increasing ProxyFailed.
	ProxyCreated, ProxyReused, ProxyFailed int
	Errors                                 []TransferError // per-item import diagnostic outcomes
}

// ProxyTransfer coordinates proxy backup mapping, per-item administration
// writes, and optional post-import probes.
type ProxyTransfer struct {
	adminService ProxyAdministrator // proxy administration write/query dependency
	// tasks owns accepted probe callbacks with independent background contexts.
	// Import copies selected IDs before registration and discards admission or
	// individual probe failures.
	tasks ProxyTasks       // optional import probe task owner
	now   func() time.Time // export timestamp clock callback
}

// NewProxyTransfer constructs backup coordination using administration,
// optional tasks, and export timing. Nil now selects time.Now, and construction
// starts no work; the task owner later joins admitted post-import probe
// callbacks.
func NewProxyTransfer(admin ProxyAdministrator, tasks ProxyTasks, now func() time.Time) *ProxyTransfer {
	if now == nil {
		now = time.Now
	}
	return &ProxyTransfer{adminService: admin, tasks: tasks, now: now}
}

// Export returns selected proxy backup values and a UTC RFC3339 export
// timestamp. Nonempty IDs choose repository-selected order and bypass query
// filters; otherwise every matching administration page is read. Credentials
// are included in the transfer records.
//
// Backup IDs become names only when that target is also in the exported set.
// Missing selected targets therefore leave BackupProxyName empty, which affects
// later restoration of fallback. Expiration is converted to Unix seconds, and
// the returned comparison keys use the connection text so destination imports
// can find matching records independently of local IDs.
func (h *ProxyTransfer) Export(ctx context.Context, query ProxyExportQuery) ([]TransferProxy, string, error) {
	var err error
	var proxies []Proxy
	if len(query.IDs) > 0 {
		proxies, err = h.getProxiesByIDs(ctx, query.IDs)
		if err != nil {
			return nil, "", err
		}
	} else {
		proxies, err = h.listProxiesFiltered(ctx, query.Protocol, query.Status, query.Search, query.SortBy, query.SortOrder)
		if err != nil {
			return nil, "", err
		}
	}

	// IDs are local to this instance. Resolve only selected backup targets to
	// names; a backup outside the exported set has no name in the resulting record.
	proxyNameByID := make(map[int64]string, len(proxies))
	for i := range proxies {
		proxyNameByID[proxies[i].ID] = proxies[i].Name
	}

	dataProxies := make([]TransferProxy, 0, len(proxies))
	for i := range proxies {
		p := proxies[i]
		key := BuildTransferProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)

		var expiresAt *int64
		if p.ExpiresAt != nil {
			// Backup timestamps use whole seconds, discarding subsecond expiration precision.
			v := p.ExpiresAt.Unix()
			expiresAt = &v
		}
		var backupProxyName string
		if p.BackupProxyID != nil {
			backupProxyName = proxyNameByID[*p.BackupProxyID]
		}
		dataProxies = append(dataProxies, TransferProxy{
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

	return dataProxies, h.now().UTC().Format(time.RFC3339), nil
}

// Import processes proxy backup items with partial created/reused/failed
// results and per-item diagnostics. A failure loading existing records aborts
// the batch; later item failures are retained, and reused IDs can be probed
// asynchronously after successful iteration.
func (h *ProxyTransfer) Import(ctx context.Context, items []TransferProxy) (ProxyImportResult, error) {
	_, result, err := h.importItems(ctx, items, false)
	return result, err
}

// ImportForProviderBinding returns a mapping containing existing destination
// proxies and successful creations for provider association lookup. It also
// returns partial proxy outcomes. Reused records are reread before status
// synchronization, status-update errors are suppressed, and reused-item probes
// are omitted.
func (h *ProxyTransfer) ImportForProviderBinding(ctx context.Context, items []TransferProxy) (map[string]int64, ProxyImportResult, error) {
	return h.importItems(ctx, items, true)
}

// importItems loads destination proxies and processes backup entries in order,
// returning key mappings and partial results. A nonempty supplied ProxyKey
// determines reuse; otherwise connection text constructs that key. It can
// therefore differ from the supplied connection values.
//
// Existing and earlier-created names resolve backup references. Reused records
// can synchronize status using imported expiration policy; providerBinding
// rereads the record and suppresses status-update errors. New items with
// unresolved backup names disable fallback and retain a diagnostic, while
// validation or create errors increment failure counts. Successful creations
// stay mapped even if later status synchronization fails.
//
// Ordinary imports queue copied reused IDs for sequential background probes
// after iteration, leaving task admission and probe failure outside the
// returned persistence outcome. Newly created records rely on CreateProxy's own
// optional probe registration. The destination list, per-item writes, and
// status updates form separate operations, so the batch has no all-or-nothing
// transaction.
func (h *ProxyTransfer) importItems(ctx context.Context, items []TransferProxy, providerBinding bool) (map[string]int64, ProxyImportResult, error) {
	result := ProxyImportResult{}

	sortBy := "id"
	if providerBinding {
		sortBy = "created_at"
	}
	existingProxies, err := h.listProxiesFiltered(ctx, "", "", "", sortBy, "desc")
	if err != nil {
		return nil, ProxyImportResult{}, err
	}

	proxyByKey := make(map[string]Proxy, len(existingProxies))
	// Names replace instance-local backup IDs. Existing records and earlier
	// creations are available to later items, while forward references cannot resolve here.
	proxyNameToID := make(map[string]int64, len(existingProxies))
	for i := range existingProxies {
		p := existingProxies[i]
		key := BuildTransferProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)
		proxyByKey[key] = p
		if p.Name != "" {
			proxyNameToID[p.Name] = p.ID
		}
	}

	latencyProbeIDs := make([]int64, 0, len(items))
	for i := range items {
		item := items[i]
		key := item.ProxyKey
		if key == "" {
			key = BuildTransferProxyKey(item.Protocol, item.Host, item.Port, item.Username, item.Password)
		}

		if err := ValidateTransferProxy(item); err != nil {
			result.ProxyFailed++
			result.Errors = append(result.Errors, TransferError{
				Kind:     "proxy",
				Name:     item.Name,
				ProxyKey: key,
				Message:  err.Error(),
			})
			continue
		}

		normalizedStatus := NormalizeTransferProxyStatus(item.Status)
		if existing, ok := proxyByKey[key]; ok {
			result.ProxyReused++
			originalID := existing.ID
			canUpdate := true
			// Provider binding uses the freshest available reused record before
			// a status patch, keeping its connection values aligned with that reread.
			if providerBinding && normalizedStatus != "" {
				fresh, err := h.adminService.GetProxy(ctx, originalID)
				if err != nil || fresh == nil {
					canUpdate = false
				} else {
					existing = *fresh
				}
			}
			if canUpdate && normalizedStatus != "" && normalizedStatus != existing.Status {
				// Status updates also apply the imported expiration and fallback settings because
				// UpdateProxy replaces those fields even when their values are zero.
				var existingExpiresAt *time.Time
				if item.ExpiresAt != nil {
					t := time.Unix(*item.ExpiresAt, 0).UTC()
					existingExpiresAt = &t
				}
				existingFallbackMode := item.FallbackMode
				if existingFallbackMode == "" {
					existingFallbackMode = FallbackModeNone
				}
				var existingBackupProxyID *int64
				if item.BackupProxyName != "" {
					if bid, ok := proxyNameToID[item.BackupProxyName]; ok {
						existingBackupProxyID = &bid
					}
				}
				updateInput := &UpdateProxyInput{
					Status:         normalizedStatus,
					ExpiresAt:      existingExpiresAt,
					FallbackMode:   existingFallbackMode,
					BackupProxyID:  existingBackupProxyID,
					ExpiryWarnDays: item.ExpiryWarnDays,
					// Keep the existing proxy's network settings when applying imported status.
					Name:     existing.Name,
					Protocol: existing.Protocol,
					Host:     existing.Host,
					Port:     existing.Port,
					Username: existing.Username,
					Password: existing.Password,
				}
				if _, err := h.adminService.UpdateProxy(ctx, originalID, updateInput); err != nil && !providerBinding {
					result.Errors = append(result.Errors, TransferError{
						Kind:     "proxy",
						Name:     item.Name,
						ProxyKey: key,
						Message:  "update status failed: " + err.Error(),
					})
				}
			}
			if !providerBinding {
				latencyProbeIDs = append(latencyProbeIDs, originalID)
			}
			continue
		}

		// Transfer expiry stores whole Unix seconds, while administration expects
		// an optional time.Time. Reconstruct it in UTC before applying expiration policy.
		var expiresAt *time.Time
		if item.ExpiresAt != nil {
			t := time.Unix(*item.ExpiresAt, 0).UTC()
			expiresAt = &t
		}

		// Resolve names using records available before this item. A later creation
		// cannot satisfy a forward reference in this single-pass destination import.
		fallbackMode := item.FallbackMode
		var backupProxyID *int64
		if item.BackupProxyName != "" {
			if bid, ok := proxyNameToID[item.BackupProxyName]; ok {
				backupProxyID = &bid
			} else {
				// A new record cannot follow an unknown destination backup. Disable
				// that fallback while retaining the missing-name diagnostic in the partial result.
				fallbackMode = FallbackModeNone
				result.Errors = append(result.Errors, TransferError{
					Kind:     "proxy",
					Name:     item.Name,
					ProxyKey: key,
					Message:  fmt.Sprintf("backup_proxy_name %q not found, fallback_mode downgraded to none", item.BackupProxyName),
				})
			}
		}

		created, err := h.adminService.CreateProxy(ctx, &CreateProxyInput{
			Name:           DefaultTransferProxyName(item.Name),
			Protocol:       item.Protocol,
			Host:           item.Host,
			Port:           item.Port,
			Username:       item.Username,
			Password:       item.Password,
			ExpiresAt:      expiresAt,
			FallbackMode:   fallbackMode,
			BackupProxyID:  backupProxyID,
			ExpiryWarnDays: item.ExpiryWarnDays,
		})
		if err != nil {
			result.ProxyFailed++
			result.Errors = append(result.Errors, TransferError{
				Kind:     "proxy",
				Name:     item.Name,
				ProxyKey: key,
				Message:  err.Error(),
			})
			continue
		}
		result.ProxyCreated++
		proxyByKey[key] = *created
		// Publish each created name after persistence, allowing later records to
		// resolve that backup identity even when no matching proxy existed initially.
		if created.Name != "" {
			proxyNameToID[created.Name] = created.ID
		}

		if normalizedStatus != "" && normalizedStatus != created.Status {
			// Pass the created expiration and fallback fields because UpdateProxy replaces zero values too.
			if _, err := h.adminService.UpdateProxy(ctx, created.ID, &UpdateProxyInput{
				Status:         normalizedStatus,
				ExpiresAt:      expiresAt,
				FallbackMode:   fallbackMode,
				BackupProxyID:  backupProxyID,
				ExpiryWarnDays: item.ExpiryWarnDays,
				Name:           created.Name,
				Protocol:       created.Protocol,
				Host:           created.Host,
				Port:           created.Port,
				Username:       created.Username,
				Password:       created.Password,
			}); err != nil && !providerBinding {
				result.Errors = append(result.Errors, TransferError{
					Kind:     "proxy",
					Name:     item.Name,
					ProxyKey: key,
					Message:  "update status failed: " + err.Error(),
				})
			}
		}
		// CreateProxy owns optional new-record probing through its own task
		// dependency. This batch registers extra probes only for reused ordinary-import IDs.
	}

	// Copy IDs before task admission so the background loop retains a stable
	// batch selection after import returns. Admission and probe errors are best effort.
	if len(latencyProbeIDs) > 0 && h.tasks != nil {
		ids := append([]int64(nil), latencyProbeIDs...)
		h.tasks.Go("handler/admin/proxy_data.go:ImportData", func() {
			for _, id := range ids {
				_, _ = h.adminService.TestProxy(context.Background(), id)
			}
		})
	}
	keys := make(map[string]int64, len(proxyByKey))
	for key, value := range proxyByKey {
		keys[key] = value.ID
	}
	return keys, result, nil
}

// getProxiesByIDs returns an empty present list for no selected IDs and
// otherwise delegates repository selection. Result order follows administration
// storage rather than input ID order, as required by proxy backup export.
func (h *ProxyTransfer) getProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	if len(ids) == 0 {
		return []Proxy{}, nil
	}
	return h.adminService.GetProxiesByIDs(ctx, ids)
}

// listProxiesFiltered reads all matching administration pages until the
// accumulated count reaches the reported total or a page is empty.
// Provider-count ordering uses its augmented listing path, while other ordering
// uses ordinary proxy listing.
//
// The helper requests pages of one thousand and strips count/probe augmentation
// from returned backup values. Each page is a separate read, so concurrent
// destination changes can affect later page contents or totals. A page failure
// returns an error and discards the accumulated result from the caller's
// export/import operation.
func (h *ProxyTransfer) listProxiesFiltered(ctx context.Context, protocol, status, search, sortBy, sortOrder string) ([]Proxy, error) {
	page := 1
	// Read bounded pages while accumulating the complete backup selection.
	pageSize := 1000
	var out []Proxy
	sortBy = strings.TrimSpace(sortBy)
	useProviderCountSort := strings.EqualFold(sortBy, "provider_count")
	for {
		if useProviderCountSort {
			items, total, err := h.adminService.ListProxiesWithProviderCount(ctx, page, pageSize, protocol, status, search, sortBy, sortOrder)
			if err != nil {
				return nil, err
			}
			for i := range items {
				out = append(out, items[i].Proxy)
			}
			if len(out) >= int(total) || len(items) == 0 {
				break
			}
		} else {
			items, total, err := h.adminService.ListProxies(ctx, page, pageSize, protocol, status, search, sortBy, sortOrder)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			if len(out) >= int(total) || len(items) == 0 {
				break
			}
		}
		page++
	}
	return out, nil
}

// GetProxiesByIDs returns selected stored proxies for provider backup
// references in repository query order. Empty input returns an empty present
// list, and the helper delegates selection to the same administration service
// as proxy export.
func (h *ProxyTransfer) GetProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	return h.getProxiesByIDs(ctx, ids)
}
