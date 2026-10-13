package routing

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// Custom model choices can include names that current provider and group rules
// cannot serve. Administrative candidates intersect the requestable patterns
// with a normalized custom list, keeping the administrator's selected order.
// When every selected name is ineligible, that intersection remains empty in
// GroupAdmin's candidate response.
//
// The same saved group settings also govern fallback validation and
// compatibility controls. An unavailable-fallback target must be saved and
// active, and a persisted source group cannot name itself as that target.
// Administrative reasoning controls are sanitized through OpenAI's effort
// vocabulary, while the legacy Messages flag follows authoritative protocol
// admission before GroupAdmin returns the group.

// ValidateUnavailableFallbackGroup requires an existing active
// unavailable-fallback target and rejects the source group itself. Pass zero
// for a group being created; platform is retained in the signature but does not
// constrain the target.
func (s *GroupAdmin) ValidateUnavailableFallbackGroup(ctx context.Context, currentGroupID int64, platform string, fallbackGroupID int64) error {
	if currentGroupID > 0 && currentGroupID == fallbackGroupID {
		return fmt.Errorf("cannot set self as unavailable fallback group")
	}
	fallbackGroup, err := s.groupRepo.GetByIDLite(ctx, fallbackGroupID)
	if err != nil {
		return fmt.Errorf("unavailable fallback group not found: %w", err)
	}
	if !fallbackGroup.IsActive() {
		return fmt.Errorf("unavailable fallback group must be active")
	}
	return nil
}

// FilterModelsListCandidates returns selected model IDs admitted by exact or
// trailing-wildcard candidate patterns, in the administrator's selected order.
// It trims and deduplicates selected names before checking them, while candidate
// patterns are trimmed for comparison.
//
// An empty candidate slice or a selection with no usable names returns nil.
// Populated inputs with no matching names return an allocated empty slice,
// allowing GroupAdmin's candidate response to retain an empty intersection.
// The returned collection has its own storage.
func FilterModelsListCandidates(candidates []string, selectedModels []string) []string {
	normalizedSelected := NormalizeGroupModelsListConfig(GroupModelsListConfig{
		Enabled: true,
		Models:  selectedModels,
	}).Models
	if len(normalizedSelected) == 0 {
		return nil
	}

	if len(candidates) == 0 {
		return nil
	}

	allowed := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			allowed = append(allowed, candidate)
		}
	}

	// Iterate selected names when intersecting with requestable patterns so the
	// model picker retains administrator order even after candidate enumeration is
	// sorted.
	filtered := make([]string, 0, len(normalizedSelected))
	for _, model := range normalizedSelected {
		if ModelsListCandidateAllowsModel(allowed, model) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// ModelsListCandidateAllowsModel reports whether an exact or trailing-wildcard
// candidate admits the selected ID. Matching is case-sensitive and inputs are
// used as supplied; a bare asterisk accepts every model.
func ModelsListCandidateAllowsModel(availablePatterns []string, model string) bool {
	for _, pattern := range availablePatterns {
		if pattern == model {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

// SanitizeGroupReasoningEffortPolicy normalizes stored effort caps and mappings
// in place using the OpenAI effort vocabulary. Nil groups are ignored; invalid
// caps are cleared, invalid mapping collections become empty, and unsupported
// over-limit actions select downgrade.
func SanitizeGroupReasoningEffortPolicy(group *Group) {
	if group == nil {
		return
	}
	maxEffort, maxErr := NormalizeMaxReasoningEffortForPlatform(PlatformOpenAI, group.MaxReasoningEffort)
	mappings, mappingsErr := NormalizeReasoningEffortMappings(PlatformOpenAI, group.ReasoningEffortMappings)
	// Invalid saved caps and mapping collections are cleared independently so one
	// unusable control does not erase another valid reasoning setting on the
	// group.
	if maxErr != nil {
		maxEffort = ""
	}
	if mappingsErr != nil {
		mappings = []ReasoningEffortMapping{}
	}
	overLimit := NormalizeMaxReasoningEffortOverLimit(group.MaxReasoningEffortOverLimit)
	// Unrecognized saved actions use downgrade, matching the administrative
	// default that caps excessive effort instead of introducing a rejection
	// policy.
	if overLimit == "" {
		overLimit = ReasoningEffortOverLimitDowngrade
	}
	group.MaxReasoningEffort = maxEffort
	group.MaxReasoningEffortOverLimit = overLimit
	group.ReasoningEffortMappings = mappings
}

// SanitizeGroupMessagesDispatchFields updates the legacy Messages flag from
// authoritative protocol admission. Nil groups are ignored, and
// DefaultMappedModel remains stored separately so this compatibility mirror
// cannot change a configured model mapping.
func SanitizeGroupMessagesDispatchFields(g *Group) {
	if g == nil {
		return
	}

	// Synchronize the compatibility mirror before GroupAdmin returns a group;
	// request admission consults AllowedProtocols, so this field follows that
	// authoritative selection.
	g.AllowMessagesDispatch = g.AllowsClientProtocol(protocol.ProtocolAnthropicMessages)
}
