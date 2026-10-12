package egress

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ProxyExportQuery struct {
	IDs                                         []int64
	Protocol, Status, Search, SortBy, SortOrder string
}
type ProxyImportResult struct {
	ProxyCreated, ProxyReused, ProxyFailed int
	Errors                                 []TransferError
}
type ProxyTransfer struct {
	adminService ProxyAdministrator
	tasks        ProxyTasks
	now          func() time.Time
}

func NewProxyTransfer(admin ProxyAdministrator, tasks ProxyTasks, now func() time.Time) *ProxyTransfer {
	if now == nil {
		now = time.Now
	}
	return &ProxyTransfer{adminService: admin, tasks: tasks, now: now}
}

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

	// Resolve backup names within the set of exported proxies.
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

// Import reports per-item failures and schedules probes for reused proxies after the batch.
func (h *ProxyTransfer) Import(ctx context.Context, items []TransferProxy) (ProxyImportResult, error) {
	_, result, err := h.importItems(ctx, items, false)
	return result, err
}

// ImportForProviderBinding rereads reused proxies before status updates and suppresses status-update errors.
func (h *ProxyTransfer) ImportForProviderBinding(ctx context.Context, items []TransferProxy) (map[string]int64, ProxyImportResult, error) {
	return h.importItems(ctx, items, true)
}

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
	// Resolve backup_proxy_name against existing proxies and earlier creations in this batch.
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

		// Convert Unix seconds to an expiration time.
		var expiresAt *time.Time
		if item.ExpiresAt != nil {
			t := time.Unix(*item.ExpiresAt, 0).UTC()
			expiresAt = &t
		}

		// Resolve backup names to IDs available at this point in the import.
		fallbackMode := item.FallbackMode
		var backupProxyID *int64
		if item.BackupProxyName != "" {
			if bid, ok := proxyNameToID[item.BackupProxyName]; ok {
				backupProxyID = &bid
			} else {
				// Disable unresolved proxy fallback and report the missing backup name.
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
		// Allow later items in the batch to reference this proxy by name.
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
		// CreateProxy already triggers a latency probe, avoid double probing here.
	}

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

func (h *ProxyTransfer) getProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	if len(ids) == 0 {
		return []Proxy{}, nil
	}
	return h.adminService.GetProxiesByIDs(ctx, ids)
}

func (h *ProxyTransfer) listProxiesFiltered(ctx context.Context, protocol, status, search, sortBy, sortOrder string) ([]Proxy, error) {
	page := 1
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

// GetProxiesByIDs retrieves the selected proxies for provider backups in repository order.
func (h *ProxyTransfer) GetProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	return h.getProxiesByIDs(ctx, ids)
}
