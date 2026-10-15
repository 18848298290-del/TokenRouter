package provider

import (
	"context"
	"fmt"
)

// management_credential_fields.go validates and applies a selected credential
// field across an administrator provider batch.
// A credential-field batch validates the chosen value and checks every
// requested provider before writing any patch. A failed or empty lookup returns
// a typed prevalidation error with its optional cause, allowing HTTP to
// distinguish that phase from per-provider storage errors during updates.
//
// Each update carries one credential key with PatchCredentials enabled.
// Configuration-aware storage merges that key into the latest locked record,
// retaining credentials that changed after prevalidation. Updates are separate
// calls and failures are collected in input order; their transaction owner
// determines which patches remain after a failure. Repeated IDs remain repeated
// operations and outcomes.

// ManagementProviderMissing carries the provider ID and optional cause of a
// failed or empty prevalidation lookup.
type ManagementProviderMissing struct {
	ID int64 // failed prevalidation provider identifier
	// Cause retains the lookup error, or nil for a successful lookup returning no
	// record. Unwrap exposes this value while Error uses ID.
	Cause error
}

// ManagementPatchItem records one requested provider's credential-field update
// outcome.
type ManagementPatchItem struct {
	ProviderID int64  // requested local provider identifier
	Success    bool   // credential field patch succeeded
	Error      string // failed credential patch message
}

// ManagementPatchResult summarizes ordered credential-field patch outcomes and
// their provider IDs.
type ManagementPatchResult struct {
	// Success and Failed count outcomes in Results. SuccessIDs and FailedIDs
	// retain the corresponding requested IDs in order, including repetitions that
	// produced repeated provider updates.
	Success, // successful credential patch count
	Failed int // failed credential patch count
	SuccessIDs, // successful requested provider identifiers
	FailedIDs []int64 // failed requested provider identifiers
	Results []ManagementPatchItem // input-ordered credential patch outcomes
}

// Error formats the prevalidation failure as a provider-not-found message for
// HTTP presentation.
func (e *ManagementProviderMissing) Error() string { return fmt.Sprintf("Provider %d not found", e.ID) }

// Unwrap returns the prevalidation lookup cause for standard error inspection.
// It returns nil when the lookup itself succeeded but supplied no record, so
// callers still use ManagementProviderMissing to classify that absence.
func (e *ManagementProviderMissing) Unwrap() error { return e.Cause }

// ValidateCredentialFieldValue accepts supported administrator credential
// fields and their permitted value types. Warmup interception requires a
// boolean; account_uuid and org_uuid accept strings or null, while every other
// field is rejected.
func ValidateCredentialFieldValue(field string, value any) error {
	switch field {
	case "intercept_warmup_requests":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("intercept_warmup_requests must be boolean")
		}
	case "account_uuid", "org_uuid":
		if value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be string or null", field)
			}
		}
	default:
		return fmt.Errorf("unsupported credential field: %s", field)
	}
	return nil
}

// PatchCredentials validates a credential field and checks all requested
// providers before submitting updates. A failed or nil lookup returns
// ManagementProviderMissing before any write begins. IDs retain their supplied
// order and repetitions.
//
// The update phase sends a one-key internal patch to each provider so
// configuration-aware storage merges with its latest locked credentials. Each
// failed update is recorded while remaining IDs continue. This method does not
// compensate successful calls when a later provider fails; their transaction
// owner controls final persistence. Counts and ID lists describe the individual
// call outcomes.
func (s *ManagementBatch) PatchCredentials(ctx context.Context, ids []int64, field string, value any) (*ManagementPatchResult, error) {
	if err := ValidateCredentialFieldValue(field, value); err != nil {
		return nil, err
	}
	for _, id := range ids {
		v, err := s.store.GetProvider(ctx, id)
		if err != nil || v == nil {
			return nil, &ManagementProviderMissing{ID: id, Cause: err}
		}
	}
	result := &ManagementPatchResult{SuccessIDs: make([]int64, 0, len(ids)), FailedIDs: make([]int64, 0, len(ids)), Results: make([]ManagementPatchItem, 0, len(ids))}
	// Prevalidation finished for every ID. Submit raw one-key patches now so
	// the configuration writer can retain credentials changed since those lookups.
	for _, id := range ids {
		_, err := s.store.UpdateProvider(ctx, id, &UpdateProviderInput{Credentials: map[string]any{field: value}, PatchCredentials: true})
		if err != nil {
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, id)
			result.Results = append(result.Results, ManagementPatchItem{ProviderID: id, Error: err.Error()})
			continue
		}
		result.Success++
		result.SuccessIDs = append(result.SuccessIDs, id)
		result.Results = append(result.Results, ManagementPatchItem{ProviderID: id, Success: true})
	}
	return result, nil
}
