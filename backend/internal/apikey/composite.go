package apikey

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

const (
	// MaxCompositeAPIKeyGroups bounds authentication snapshots and aggregated model lists.
	MaxCompositeAPIKeyGroups = 20
	MaxCompositeKeyPrefixLen = 32
)

var KeyCompositeKeyPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// APIKeyCompositeGroupInput configures a mapping during key creation or update.
type APIKeyCompositeGroupInput struct {
	GroupID int64  `json:"group_id"`
	Prefix  string `json:"prefix"`
}

// AllowsRuntimeGroup restricts composite keys to bound groups. Callers separately check user permissions.
func (k *APIKey) AllowsRuntimeGroup(groupID int64) bool {
	if k == nil || groupID <= 0 {
		return false
	}
	if !k.IsComposite {
		return true
	}
	for _, binding := range k.CompositeGroups {
		if binding.GroupID == groupID {
			return true
		}
	}
	return false
}

// NormalizeCompositeKeyPrefix validates a trimmed prefix and returns its case-folded lookup form.
func NormalizeCompositeKeyPrefix(prefix string) (display string, normalized string, ok bool) {
	display = strings.TrimSpace(prefix)
	if display == "" || len(display) > MaxCompositeKeyPrefixLen || !KeyCompositeKeyPrefixPattern.MatchString(display) {
		return "", "", false
	}
	return display, strings.ToLower(display), true
}

// SplitCompositeModel splits at the first slash so model IDs can contain further slashes.
func SplitCompositeModel(model string) (prefix string, modelID string, ok bool) {
	model = strings.TrimSpace(model)
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	prefix = strings.TrimSpace(parts[0])
	modelID = strings.TrimSpace(parts[1])
	if prefix == "" || modelID == "" {
		return "", "", false
	}
	return prefix, modelID, true
}

// ResolveCompositeModel selects a group by case-insensitive prefix and returns the unprefixed model ID.
func (k *APIKey) ResolveCompositeModel(model string) (*APIKeyCompositeGroup, string, error) {
	if k == nil || !k.IsComposite {
		return nil, model, nil
	}
	prefix, modelID, ok := SplitCompositeModel(model)
	if !ok {
		return nil, "", ErrCompositeKeyPrefixRequired
	}
	_, normalized, valid := NormalizeCompositeKeyPrefix(prefix)
	if !valid {
		return nil, "", ErrCompositeKeyPrefixInvalid
	}
	for i := range k.CompositeGroups {
		binding := &k.CompositeGroups[i]
		if binding.NormalizedPrefix == normalized {
			return binding, modelID, nil
		}
	}
	return nil, "", ErrCompositeKeyPrefixNotFound
}

// KeyValidateCompositeGroupInputs validates mapping structure and retains input order.
func KeyValidateCompositeGroupInputs(inputs []APIKeyCompositeGroupInput) ([]APIKeyCompositeGroup, error) {
	if len(inputs) == 0 {
		return nil, ErrCompositeKeyGroupsRequired
	}
	if len(inputs) > MaxCompositeAPIKeyGroups {
		return nil, ErrCompositeKeyTooManyGroups
	}
	groups := make([]APIKeyCompositeGroup, 0, len(inputs))
	seenGroups := make(map[int64]struct{}, len(inputs))
	seenPrefixes := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		if input.GroupID <= 0 {
			return nil, ErrGroupNotAllowed
		}
		if _, exists := seenGroups[input.GroupID]; exists {
			return nil, ErrCompositeKeyGroupDuplicate
		}
		display, normalized, ok := NormalizeCompositeKeyPrefix(input.Prefix)
		if !ok {
			return nil, ErrCompositeKeyPrefixInvalid
		}
		if _, exists := seenPrefixes[normalized]; exists {
			return nil, ErrCompositeKeyPrefixDuplicate
		}
		seenGroups[input.GroupID] = struct{}{}
		seenPrefixes[normalized] = struct{}{}
		groups = append(groups, APIKeyCompositeGroup{
			GroupID:          input.GroupID,
			Prefix:           display,
			NormalizedPrefix: normalized,
			SortOrder:        index,
		})
	}
	return groups, nil
}

// KeyPrepareCompositeGroups checks group permissions and loads group snapshots.
func (s *APIKeyService) KeyPrepareCompositeGroups(
	ctx context.Context,
	user *User,
	inputs []APIKeyCompositeGroupInput,
) ([]APIKeyCompositeGroup, error) {
	bindings, err := KeyValidateCompositeGroupInputs(inputs)
	if err != nil {
		return nil, err
	}
	for i := range bindings {
		binding := &bindings[i]
		group, err := s.groupRepo.GetByID(ctx, binding.GroupID)
		if err != nil {
			return nil, err
		}
		if !s.KeyCanUserBindGroup(ctx, user, group) {
			return nil, ErrGroupNotAllowed
		}
		binding.Group = group
	}
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].SortOrder < bindings[j].SortOrder })
	return bindings, nil
}

// KeyCloneCompositeBindings copies the binding slice; referenced group data remains shared.
func KeyCloneCompositeBindings(bindings []APIKeyCompositeGroup) []APIKeyCompositeGroup {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]APIKeyCompositeGroup, len(bindings))
	copy(out, bindings)
	return out
}
