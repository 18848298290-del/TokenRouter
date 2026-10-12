package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Import processes entries independently and returns per-entry failures in the result.
// A failure to load the existing-provider index aborts the import with an error.
func (h *CodexImporter) Import(ctx context.Context, req CodexSessionImportRequest, entries []CodexImportEntry) (CodexSessionImportResult, error) {
	result := CodexSessionImportResult{
		Total: len(entries),
		Items: make([]CodexSessionImportItem, 0, len(entries)),
	}

	existingProviders, err := h.archive.ListProvidersForImport(ctx, PlatformOpenAI, ProviderTypeOAuth, "", "", 0, "", "created_at", "desc")
	if err != nil {
		return result, err
	}
	index := BuildCodexProviderIndex(existingProviders)

	updateExisting := true
	if req.UpdateExisting != nil {
		updateExisting = *req.UpdateExisting
	}
	concurrency := 3
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}
	credentialExtras := SanitizeCodexImportCredentialExtras(req.CredentialExtras)

	seenIdentity := map[string]CodexSeenIdentity{}
	for _, entry := range entries {
		item, err := NormalizeCodexImportEntry(entry, h.options)
		if err != nil {
			result.Failed++
			result.Items = append(result.Items, CodexSessionImportItem{
				Index:   entry.Index,
				Action:  "failed",
				Message: err.Error(),
			})
			result.Errors = append(result.Errors, CodexSessionImportMessage{
				Index:   entry.Index,
				Message: err.Error(),
			})
			continue
		}
		providerName := buildCodexCreateProviderName(req.Name, item, entry.Index, len(entries))
		effectiveExpiresAt, credentialExpiresAt, autoPauseOnExpired, expiryWarnings, expiryErr := ResolveCodexImportExpiry(req, item, h.options.Now)
		if expiryErr != nil {
			result.Failed++
			result.Items = append(result.Items, CodexSessionImportItem{
				Index:   entry.Index,
				Name:    providerName,
				Action:  "failed",
				Message: expiryErr.Error(),
			})
			result.Errors = append(result.Errors, CodexSessionImportMessage{
				Index:   entry.Index,
				Name:    providerName,
				Message: expiryErr.Error(),
			})
			continue
		}
		item.WarningTexts = append(item.WarningTexts, expiryWarnings...)
		if credentialExpiresAt != nil {
			item.Credentials["expires_at"] = credentialExpiresAt.Format(time.RFC3339)
		}
		credentials := MergeCodexImportMap(item.Credentials, credentialExtras)
		extra := MergeCodexImportMap(req.Extra, item.Extra)
		for _, warning := range item.WarningTexts {
			result.Warnings = append(result.Warnings, CodexSessionImportMessage{
				Index:   entry.Index,
				Name:    providerName,
				Message: warning,
			})
		}

		if duplicateIndex, ok := FirstSeenCodexIdentity(seenIdentity, item.IdentityKeys, item.UserID); ok {
			message := fmt.Sprintf("与第 %d 条导入项重复，已跳过", duplicateIndex)
			result.Skipped++
			result.Items = append(result.Items, CodexSessionImportItem{
				Index:   entry.Index,
				Name:    providerName,
				Action:  "skipped",
				Message: message,
			})
			result.Warnings = append(result.Warnings, CodexSessionImportMessage{
				Index:   entry.Index,
				Name:    providerName,
				Message: message,
			})
			continue
		}
		MarkCodexIdentitySeen(seenIdentity, item.IdentityKeys, entry.Index, item.UserID)

		existing, matchedKey := index.Find(item.IdentityKeys, item.UserID)
		if existing != nil && updateExisting {
			if strings.HasPrefix(matchedKey, "provider:") && item.UserID != "" &&
				CodexCredentialString(existing.Credentials, "chatgpt_user_id") == "" {
				result.Warnings = append(result.Warnings, CodexSessionImportMessage{
					Index:   entry.Index,
					Name:    providerName,
					Message: "已有提供商未记录 chatgpt_user_id，已按共享的 chatgpt_account_id 匹配并回填，请确认两者属于同一用户",
				})
			}
			preserveExistingRefresh := item.RefreshToken == "" &&
				CodexCredentialString(existing.Credentials, "refresh_token") != ""
			if preserveExistingRefresh {
				result.Warnings = append(result.Warnings, CodexSessionImportMessage{
					Index:   entry.Index,
					Name:    providerName,
					Message: "已有提供商包含 refresh_token，本次 accessToken-only 导入已保留自动续期凭据",
				})
				effectiveExpiresAt = nil
				autoPauseOnExpired = nil
			}
			mergedCredentials := MergeCodexImportCredentials(existing.Credentials, credentials, item)
			mergedExtra := MergeCodexImportMap(existing.Extra, extra)
			updateInput := &UpdateProviderInput{
				Credentials:        mergedCredentials,
				Extra:              mergedExtra,
				Concurrency:        req.Concurrency,
				Priority:           req.Priority,
				RateMultiplier:     req.RateMultiplier,
				LoadFactor:         req.LoadFactor,
				ExpiresAt:          effectiveExpiresAt,
				AutoPauseOnExpired: autoPauseOnExpired,
			}
			if req.ProxyID != nil {
				updateInput.ProxyID = req.ProxyID
			}
			if len(req.GroupIDs) > 0 {
				groupIDs := append([]int64(nil), req.GroupIDs...)
				updateInput.GroupIDs = &groupIDs
			}
			updated, updateErr := h.providers.UpdateProvider(ctx, existing.ID, updateInput)
			if updateErr != nil {
				result.Failed++
				result.Items = append(result.Items, CodexSessionImportItem{
					Index:   entry.Index,
					Name:    providerName,
					Action:  "failed",
					Message: updateErr.Error(),
				})
				result.Errors = append(result.Errors, CodexSessionImportMessage{
					Index:   entry.Index,
					Name:    providerName,
					Message: updateErr.Error(),
				})
				continue
			}
			if h.options.Invalidate != nil && updated != nil {
				_ = h.options.Invalidate(ctx, updated)
			}
			result.Updated++
			providerID := existing.ID
			if updated != nil {
				providerID = updated.ID
				index.Add(*updated)
			}
			result.Items = append(result.Items, CodexSessionImportItem{
				Index:      entry.Index,
				Name:       providerName,
				Action:     "updated",
				ProviderID: providerID,
			})
			continue
		}

		provider, createErr := h.providers.CreateProvider(ctx, &CreateProviderInput{
			Name:               providerName,
			Notes:              req.Notes,
			Platform:           PlatformOpenAI,
			Type:               ProviderTypeOAuth,
			Credentials:        credentials,
			Extra:              extra,
			ProxyID:            req.ProxyID,
			Concurrency:        concurrency,
			Priority:           priority,
			RateMultiplier:     req.RateMultiplier,
			LoadFactor:         req.LoadFactor,
			GroupIDs:           req.GroupIDs,
			ExpiresAt:          effectiveExpiresAt,
			AutoPauseOnExpired: autoPauseOnExpired,
		})
		if createErr != nil {
			result.Failed++
			result.Items = append(result.Items, CodexSessionImportItem{
				Index:   entry.Index,
				Name:    providerName,
				Action:  "failed",
				Message: createErr.Error(),
			})
			result.Errors = append(result.Errors, CodexSessionImportMessage{
				Index:   entry.Index,
				Name:    providerName,
				Message: createErr.Error(),
			})
			continue
		}
		if provider != nil {
			index.Add(*provider)
		}
		result.Created++
		providerID := int64(0)
		if provider != nil {
			providerID = provider.ID
		}
		result.Items = append(result.Items, CodexSessionImportItem{
			Index:      entry.Index,
			Name:       providerName,
			Action:     "created",
			ProviderID: providerID,
		})
	}

	return result, nil
}
