package apikey

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// composite.go validates composite Key bindings and resolves model prefixes to
// their routing groups. Prefix matching is case insensitive, while the
// remaining model ID retains any further slashes for later protocol and model
// processing. The request resolver returns the matched binding inside the
// Key's slice, so callers treat that binding as input when creating an
// effective request view.
//
// Creation and replacement validate the entire binding list before loading
// group permissions. Duplicate group IDs and repeated lowercase prefixes are
// rejected, and submitted order becomes SortOrder for storage and model
// enumeration. Runtime scope checks allow ordinary keys to consider positive
// target IDs, while composite keys require a saved binding; payer permission
// remains a separate admission decision.

const (
	// Composite configuration bounds constrain serialized authentication data
	// and accepted prefix syntax before group permission or model-list work.
	MaxCompositeAPIKeyGroups = 20 // maximum selectable group bindings
	MaxCompositeKeyPrefixLen = 32 // maximum ASCII prefix bytes
)

// KeyCompositeKeyPrefixPattern defines ASCII prefix characters accepted for
// persisted composite bindings and request lookup.
var KeyCompositeKeyPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// APIKeyCompositeGroupInput supplies a target group and its displayed selection
// prefix.
type APIKeyCompositeGroupInput struct {
	GroupID int64  `json:"group_id"` // requested target group identifier
	Prefix  string `json:"prefix"`   // submitted group selection prefix
}

// AllowsRuntimeGroup reports whether a positive target ID fits this Key's
// binding scope. Nil keys reject selection; ordinary keys accept positive IDs,
// and composite keys require a matching saved group before separate payer
// checks.
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

// NormalizeCompositeKeyPrefix trims prefix and returns display and lowercase
// lookup text when valid. Empty, oversized, or non-ASCII-prefix input returns
// empty results with false; callers use the normalized result for duplicate
// checks and lookup.
func NormalizeCompositeKeyPrefix(prefix string) (display string, normalized string, ok bool) {
	display = strings.TrimSpace(prefix)
	if display == "" || len(display) > MaxCompositeKeyPrefixLen || !KeyCompositeKeyPrefixPattern.MatchString(display) {
		return "", "", false
	}
	return display, strings.ToLower(display), true
}

// SplitCompositeModel trims model and separates a prefix from the remaining
// model ID at the first slash. Missing separators or empty trimmed parts return
// empty results with false.
//
// Later slashes belong to the model ID, so GPT/vendor/model becomes prefix GPT
// with model ID vendor/model. Whitespace around either part is removed before
// request selection, allowing the binding lookup to normalize its prefix while
// the model processing receives the complete remaining ID.
func SplitCompositeModel(model string) (prefix string, modelID string, ok bool) {
	model = strings.TrimSpace(model)
	// Split once because provider model IDs can contain additional slashes;
	// those remaining segments identify the model after the group is selected.
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

// ResolveCompositeModel returns the matched binding and unprefixed model ID for
// a composite request. A nil or ordinary Key returns the supplied model
// unchanged with no binding; composite requests distinguish missing, invalid,
// and unknown prefixes.
//
// Lookup compares the normalized request prefix with saved NormalizedPrefix
// values in binding order. The result points into CompositeGroups and shares
// its group data, so callers create their request selection view before
// changing binding or group state. Remaining slashes stay in the returned model
// ID for later handling.
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

// KeyValidateCompositeGroupInputs validates count, positive group IDs, and
// normalized prefix uniqueness for the complete list. It returns new binding
// records in submitted order with SortOrder indices; empty input is rejected,
// and group permission is checked by KeyPrepareCompositeGroups afterward.
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

// KeyPrepareCompositeGroups validates binding input and loads each target group
// for the supplied payer. A failed lookup or binding permission rejects the
// whole result; successful records carry the loaded group pointers and retain
// submitted order.
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

// KeyCloneCompositeBindings copies the binding slice while retaining group and
// RPM override pointers. Nil and empty inputs both return nil; callers treat
// referenced group and override data as shared after this copy.
func KeyCloneCompositeBindings(bindings []APIKeyCompositeGroup) []APIKeyCompositeGroup {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]APIKeyCompositeGroup, len(bindings))
	copy(out, bindings)
	return out
}
