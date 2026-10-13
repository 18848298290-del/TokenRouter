package routing

import (
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Preferred providers can be configured for an exact model or a trailing
// wildcard. A populated exact entry takes precedence, while an empty exact
// entry lets a matching pattern supply providers; the selected IDs retain their
// configured order.
//
// A group carries admission and product settings beside those preferences.
// Gateway and scheduler consumers still check current provider eligibility
// before using a preferred ID, and provider credentials determine execution
// capabilities. Shared accessview types carry these settings into authentication
// snapshots, while CloneGroup delegates their collection and optional-value
// copying to accessview when a caller retains or edits a group.

// Group scheduler names select basic or advanced provider ordering after
// admission checks complete.
const (
	// GroupSchedulerTypeBasic selects basic provider ordering.
	GroupSchedulerTypeBasic GroupSchedulerType = "basic"
	// GroupSchedulerTypeAdvanced selects the shared advanced scoring rules.
	GroupSchedulerTypeAdvanced GroupSchedulerType = "advanced"

	// Group statuses distinguish enabled routing from administrative disablement,
	// while platform aliases identify provider capabilities shared with other
	// routing code.
	StatusActive        = "active"
	StatusDisabled      = "disabled"
	PlatformOpenAI      = capability.PlatformOpenAI
	PlatformAnthropic   = capability.PlatformAnthropic
	PlatformGemini      = capability.PlatformGemini
	PlatformAntigravity = capability.PlatformAntigravity
	PlatformQoder       = capability.PlatformQoder
	PlatformGrok        = capability.PlatformGrok
	PlatformKimi        = capability.PlatformKimi
	PlatformZhipu       = capability.PlatformZhipu
	PlatformDeepseek    = capability.PlatformDeepseek
	// Feature keys identify optional compatibility settings in a group's
	// FeaturesConfig object for their respective policy readers.
	featureKeyBedrockCCCompat    = "bedrock_cc_compat"
	featureKeyWebSearchEmulation = "web_search_emulation"
)

// GroupModelsListConfig selects an ordered custom catalog of request model IDs.
type GroupModelsListConfig = accessview.GroupModelsListConfig

// GroupAvailabilityProbeConfig contains scheduling and request settings for
// active availability probes.
type GroupAvailabilityProbeConfig = accessview.GroupAvailabilityProbeConfig

// GroupAdvancedSchedulerOverrides supplies sparse values that override the
// shared advanced scheduler settings.
type GroupAdvancedSchedulerOverrides = policy.GroupAdvancedSchedulerOverrides

// GroupSchedulerType identifies the provider selection algorithm configured for
// a group.
type GroupSchedulerType = accessview.GroupSchedulerType

// GroupRoutingPolicy contains model mappings, admission restrictions, and
// stored feature settings.
type GroupRoutingPolicy = accessview.GroupRoutingPolicy

// Group is the administrative and request-routing view of a saved group
// configuration.
type Group accessview.GroupConfig

// NormalizeGroupSchedulerType accepts the basic or advanced scheduler and
// returns its canonical name. Surrounding whitespace and letter case are
// normalized; an empty value selects basic, and every other value returns an
// error.
func NormalizeGroupSchedulerType(value string) (GroupSchedulerType, error) {
	normalized := GroupSchedulerType(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "", GroupSchedulerTypeBasic:
		return GroupSchedulerTypeBasic, nil
	case GroupSchedulerTypeAdvanced:
		return GroupSchedulerTypeAdvanced, nil
	default:
		return "", fmt.Errorf("scheduler_type must be basic or advanced")
	}
}

// UsesAdvancedScheduler reports whether this group selects the advanced
// scheduler. A nil group returns false; the stored value must already equal the
// canonical advanced name for this predicate to enable advanced selection.
func (g *Group) UsesAdvancedScheduler() bool {
	return g != nil && g.SchedulerType == GroupSchedulerTypeAdvanced
}

// IsActive reports whether the stored status enables this group.
func (g *Group) IsActive() bool {
	return g.Status == StatusActive
}

// IsGroupContextValid checks whether a loaded group contains the minimum
// routing context. It requires a positive ID, Hydrated, and a nonempty status;
// callers still check activity and request-specific permissions separately.
func IsGroupContextValid(group *Group) bool {
	if group == nil {
		return false
	}
	if group.ID <= 0 {
		return false
	}
	if !group.Hydrated {
		return false
	}
	if group.Status == "" {
		return false
	}
	return true
}

// GetRoutingProviderIDs returns preferred providers for a model, retaining each
// rule's provider order. A disabled switch, empty rules, or an empty model
// returns nil, and callers supply a non-nil group.
//
// A populated exact rule wins. An empty exact rule leaves matching wildcards
// eligible, and the first populated wildcard found in map iteration wins, so
// overlapping patterns have no stable priority. The returned slice shares group
// storage and callers keep it unchanged.
func (g *Group) GetRoutingProviderIDs(requestedModel string) []int64 {
	if !g.ModelRoutingEnabled || len(g.ModelRouting) == 0 || requestedModel == "" {
		return nil
	}

	// Ignore empty exact preferences so a matching populated pattern can still
	// supply candidates before selectors try their ordinary provider pool.
	if providerIDs, ok := g.ModelRouting[requestedModel]; ok && len(providerIDs) > 0 {
		return providerIDs
	}

	// Wildcard rules are evaluated directly from the saved map. Administrators who
	// need a predictable preferred subset can provide a populated exact model entry.
	for pattern, providerIDs := range g.ModelRouting {
		if MatchModelPattern(pattern, requestedModel) && len(providerIDs) > 0 {
			return providerIDs
		}
	}

	return nil
}

// MatchModelPattern compares a model ID with an exact or trailing-asterisk
// pattern. Matching is case-sensitive, a bare asterisk accepts every model, and
// asterisks before the final position remain literal prefix characters.
func MatchModelPattern(pattern, model string) bool {
	if pattern == model {
		return true
	}

	if before, ok := strings.CutSuffix(pattern, "*"); ok {
		prefix := before
		return strings.HasPrefix(model, prefix)
	}

	return false
}

// CloneGroup copies the group using accessview.CloneGroupConfig and returns nil
// for a nil input. Declared collections, optional scalar pointers, and scheduler
// overrides receive separate storage. GroupRoutingPolicy.Clone also copies its
// supported nested feature shapes; other dynamic feature values can retain
// references, and callers keep those shared values immutable.
func CloneGroup(g *Group) *Group {
	return (*Group)(accessview.CloneGroupConfig((*accessview.GroupConfig)(g)))
}

// GroupAllowsResponsesImages performs the legacy image-admission check used by
// Responses and WebSocket adapters. Nil groups and groups with a nonempty
// ResponsesImagePolicy pass this check so adapters can apply that policy
// separately. When the named policy is blank, AllowImageGeneration supplies the
// result.
func GroupAllowsResponsesImages(group *Group) bool {
	return group == nil || group.ResponsesImagePolicy != "" || group.AllowImageGeneration
}
