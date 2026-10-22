package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// codex_import_flow.go imports normalized Codex sessions by matching existing
// providers, applying expiry rules, and recording per-entry outcomes.
// Codex session imports load one stored-provider population, then normalize
// and persist each entry independently. Fatal initial lookup failure aborts
// the batch, while normalization, expiry, and row-write failures become item
// results and leave already completed rows in place.
//
// Identity is marked seen before persistence, so a failed item can still
// suppress a later same-batch duplicate. Existing-row updates retain refresh
// credentials omitted by access-only input and remove its new provider-expiry
// pause override in that case. Token invalidation follows accepted updates and
// is best effort. Successful nonnil returned records update the index for
// later matching; nil successful results retain the existing row ID for
// updates or zero for creation. Callers supply stable request values and
// initialized importer dependencies.

// Import normalizes and saves stable entries against one loaded OpenAI OAuth
// population. Initial archive failure aborts, while individual failures
// accumulate and earlier accepted writes remain committed.
//
// Full OAuth, access-only, and Agent Identity keys follow their distinct
// matching policies. Seen identity is reserved before create/update, even when
// that write later fails. Updates merge supported credential/Extra values,
// retain omitted existing renewal fields, and avoid a new access-only expiry
// pause when renewal remains available. Optional invalidation errors do not
// reverse success. Returned records refresh the local index; nil successful
// records use the prior update ID or zero creation ID. Defaults apply only
// where creation owns them, and the caller retains request synchronization
// throughout processing.
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
			message := fmt.Sprintf("Duplicate of import entry %d, skipped", duplicateIndex)
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
		// Reserve identity before persistence so later same-batch duplicates are
		// skipped even if this item’s create or update fails. The result records
		// that failed first attempt independently.
		MarkCodexIdentitySeen(seenIdentity, item.IdentityKeys, entry.Index, item.UserID)

		existing, matchedKey := index.Find(item.IdentityKeys, item.UserID)
		if existing != nil && updateExisting {
			if strings.HasPrefix(matchedKey, "provider:") && item.UserID != "" &&
				CodexCredentialString(existing.Credentials, "chatgpt_user_id") == "" {
				result.Warnings = append(result.Warnings, CodexSessionImportMessage{
					Index:   entry.Index,
					Name:    providerName,
					Message: "Existing provider was matched by shared chatgpt_account_id and its missing chatgpt_user_id was filled in. Verify that both belong to the same user",
				})
			}
			preserveExistingRefresh := item.RefreshToken == "" &&
				CodexCredentialString(existing.Credentials, "refresh_token") != ""
			// Existing renewal remains usable when access-only input omits it.
			// Clear this input’s provider-expiry/pause overrides so the update does
			// not turn that renewable row into an access-only scheduling lifetime.
			if preserveExistingRefresh {
				result.Warnings = append(result.Warnings, CodexSessionImportMessage{
					Index:   entry.Index,
					Name:    providerName,
					Message: "Existing provider has a refresh_token; this accessToken-only import retained it for automatic renewal",
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
			// Invalidate after the accepted row update. Failure leaves persisted
			// credentials in place and does not change this item’s success count.
			if h.options.Invalidate != nil && updated != nil {
				_ = h.options.Invalidate(ctx, updated)
			}
			result.Updated++
			providerID := existing.ID
			if updated != nil {
				providerID = updated.ID
				// Returned identities replace stored candidates
				// after persistence, so later entries match the
				// accepted row data rather than stale keys.
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
		// A nonnil created row contributes its identities only after accepted
		// persistence. Nil successful data still counts creation but adds no
		// row-specific matching information to this batch’s index.
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
