package creative

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Creative model settings identify exact group and model pairs, with
// operations stored in a stable product order. Validation trims model names,
// normalizes operation spelling, and rejects repeated pairs before JSON is
// produced for persistence. The empty list remains an empty allowlist for
// public discovery and submission.
//
// Saving through Public additionally checks each pair against currently
// readable provider routes. Known routes narrow the configured operations to
// their resolved capabilities, while a missing group or unresolved model keeps
// its valid stored entry available for later recovery. A provider or attribute
// lookup error encountered during route resolution aborts that save instead of
// publishing partly checked results.

// CreativeOperationOrder orders normalized settings and operation intersections
// as generate, edit, then inpaint for persisted values and client capability lists.
var CreativeOperationOrder = []string{
	CreativeOperationGenerate,
	CreativeOperationEdit,
	CreativeOperationInpaint,
}

// CreativeModelSetting is an administrator allowlist entry for a group, model,
// and operations.
type CreativeModelSetting struct {
	GroupID int64  `json:"group_id"` // configured target group identifier
	Model   string `json:"model"`    // exact configured request model
	// configured operation allowlist names
	Operations []string `json:"operations"`
}

// CreativeModelCandidate is an image model available for administrator selection.
type CreativeModelCandidate struct {
	GroupID   int64  `json:"group_id"`   // candidate owning group identifier
	GroupName string `json:"group_name"` // candidate group display name
	Platform  string `json:"platform"`   // selected image route platform
	Model     string `json:"model"`      // candidate public request model
	// currently resolved candidate operations
	Operations []string `json:"operations"`
}

// NormalizeCreativeModelSettingsForSave validates entries and narrows
// operations for models with currently resolved routes. Syntax normalization
// runs even when the Public receiver or group reader is unavailable, in which
// case that normalized list is returned directly.
//
// One group and provider query is reused for each group in the input. A group
// lookup failure or unresolved model retains the normalized entry, while
// route-resolution failures abort the save. An entry whose resolved operation
// intersection becomes empty is removed, allowing the administrator settings
// to reflect currently known platform capabilities.
func (s *Public) NormalizeCreativeModelSettingsForSave(ctx context.Context, input []CreativeModelSetting) ([]CreativeModelSetting, error) {
	normalized, err := NormalizeCreativeModelSettings(input)
	if err != nil || s == nil || s.GroupRepo == nil {
		return normalized, err
	}
	out := make([]CreativeModelSetting, 0, len(normalized))
	queries := make(map[int64]*creativeModelQuery)
	for _, item := range normalized {
		query, loaded := queries[item.GroupID]
		if !loaded {
			group, lookupErr := s.GroupRepo.GetByIDLite(ctx, item.GroupID)
			if lookupErr == nil && group != nil {
				var routeErr error
				query, routeErr = s.prepareCreativeModels(ctx, group)
				if routeErr != nil {
					return nil, routeErr
				}
			}
			// Cache unavailable groups as nil too, so entries
			// sharing this group reuse
			// the same unresolved result and remain eligible for a
			// later settings save.
			queries[item.GroupID] = query
		}
		if route, ok, err := query.resolve(ctx, item.Model, false); err != nil {
			return nil, err
		} else if ok {
			item.Operations = intersectCreativeOperations(item.Operations, route.Operations)
		}
		if len(item.Operations) > 0 {
			out = append(out, item)
		}
	}
	return out, nil
}

// NormalizeCreativeModelSettings returns newly allocated entries with trimmed
// models and deduplicated, ordered operations. Group identifiers must be
// positive, every entry needs an operation, and repeated exact group/model
// pairs fail the whole normalization.
func NormalizeCreativeModelSettings(input []CreativeModelSetting) ([]CreativeModelSetting, error) {
	out := make([]CreativeModelSetting, 0, len(input))
	seenModels := make(map[string]struct{}, len(input))
	for index, item := range input {
		if item.GroupID <= 0 {
			return nil, fmt.Errorf("creative model setting %d group_id must be positive", index)
		}
		model := strings.TrimSpace(item.Model)
		if model == "" {
			return nil, fmt.Errorf("creative model setting %d model is required", index)
		}

		configured := make(map[string]struct{}, len(item.Operations))
		for _, operation := range item.Operations {
			operation = strings.ToLower(strings.TrimSpace(operation))
			switch operation {
			case CreativeOperationGenerate, CreativeOperationEdit, CreativeOperationInpaint:
				configured[operation] = struct{}{}
			default:
				return nil, fmt.Errorf("creative model setting %d operation %q is invalid", index, operation)
			}
		}
		if len(configured) == 0 {
			return nil, fmt.Errorf("creative model setting %d must contain at least one operation", index)
		}

		modelKey := fmt.Sprintf("%d:%s", item.GroupID, model)
		if _, exists := seenModels[modelKey]; exists {
			return nil, fmt.Errorf("creative model setting for group %d model %q is duplicated", item.GroupID, model)
		}
		seenModels[modelKey] = struct{}{}

		operations := make([]string, 0, len(configured))
		for _, operation := range CreativeOperationOrder {
			if _, ok := configured[operation]; ok {
				operations = append(operations, operation)
			}
		}
		out = append(out, CreativeModelSetting{
			GroupID:    item.GroupID,
			Model:      model,
			Operations: operations,
		})
	}
	return out, nil
}

// MarshalCreativeModelSettings returns validated entries and their stable JSON
// representation for settings persistence. Invalid entries stop encoding; an
// empty input is represented as an empty JSON array.
func MarshalCreativeModelSettings(input []CreativeModelSetting) (string, []CreativeModelSetting, error) {
	normalized, err := NormalizeCreativeModelSettings(input)
	if err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", nil, fmt.Errorf("marshal creative model settings: %w", err)
	}
	return string(raw), normalized, nil
}

// CreativeModelSettingsIndex keys operations by decimal group identifier and
// trimmed model name. Operation slices are copied, and later entries for the
// same key replace earlier entries in the returned map.
func CreativeModelSettingsIndex(settings []CreativeModelSetting) map[string][]string {
	index := make(map[string][]string, len(settings))
	for _, setting := range settings {
		key := fmt.Sprintf("%d:%s", setting.GroupID, strings.TrimSpace(setting.Model))
		index[key] = append([]string(nil), setting.Operations...)
	}
	return index
}

// CreativeOperationsForModel returns configured operations supported by the
// supplied route capabilities. The boolean reports whether the exact
// group/model key exists, even when its operation intersection is empty.
func CreativeOperationsForModel(settings map[string][]string, groupID int64, model string, supported []string) ([]string, bool) {
	configured, ok := settings[fmt.Sprintf("%d:%s", groupID, strings.TrimSpace(model))]
	if !ok {
		return nil, false
	}
	supportedSet := make(map[string]struct{}, len(supported))
	for _, operation := range supported {
		supportedSet[operation] = struct{}{}
	}
	operations := make([]string, 0, len(configured))
	for _, operation := range CreativeOperationOrder {
		if ContainsCreativeOperation(configured, operation) {
			if _, supported := supportedSet[operation]; supported {
				operations = append(operations, operation)
			}
		}
	}
	return operations, true
}

// ContainsCreativeOperation reports whether values contain the exact operation
// spelling supplied.
func ContainsCreativeOperation(values []string, target string) bool {
	return slices.Contains(values, target)
}
