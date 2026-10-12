package routing

import (
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

const (
	// GroupSchedulerTypeBasic selects the basic scheduler.
	GroupSchedulerTypeBasic GroupSchedulerType = "basic"
	// GroupSchedulerTypeAdvanced selects the shared advanced scheduler.
	GroupSchedulerTypeAdvanced GroupSchedulerType = "advanced"

	StatusActive                 = "active"
	StatusDisabled               = "disabled"
	PlatformOpenAI               = capability.PlatformOpenAI
	PlatformAnthropic            = capability.PlatformAnthropic
	PlatformGemini               = capability.PlatformGemini
	PlatformAntigravity          = capability.PlatformAntigravity
	PlatformQoder                = capability.PlatformQoder
	PlatformGrok                 = capability.PlatformGrok
	PlatformKimi                 = capability.PlatformKimi
	PlatformZhipu                = capability.PlatformZhipu
	PlatformDeepseek             = capability.PlatformDeepseek
	featureKeyBedrockCCCompat    = "bedrock_cc_compat"
	featureKeyWebSearchEmulation = "web_search_emulation"
)

type GroupModelsListConfig = accessview.GroupModelsListConfig

type GroupAvailabilityProbeConfig = accessview.GroupAvailabilityProbeConfig

type GroupAdvancedSchedulerOverrides = policy.GroupAdvancedSchedulerOverrides

type GroupSchedulerType = accessview.GroupSchedulerType

// GroupRoutingPolicy is the shared group routing configuration.
type GroupRoutingPolicy = accessview.GroupRoutingPolicy

type Group accessview.GroupConfig

// NormalizeGroupSchedulerType validates the scheduler name and defaults an empty value to basic.
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

// UsesAdvancedScheduler reports whether the group enables advanced provider selection.
func (g *Group) UsesAdvancedScheduler() bool {
	return g != nil && g.SchedulerType == GroupSchedulerTypeAdvanced
}

func (g *Group) IsActive() bool {
	return g.Status == StatusActive
}

// IsGroupContextValid checks that the group was loaded with the fields needed for routing.
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

// GetRoutingProviderIDs returns preferred providers for the requested model, or nil when no rule applies.
// The returned slice belongs to the group and must be treated as read-only.
func (g *Group) GetRoutingProviderIDs(requestedModel string) []int64 {
	if !g.ModelRoutingEnabled || len(g.ModelRouting) == 0 || requestedModel == "" {
		return nil
	}

	// A populated exact match takes precedence.
	if providerIDs, ok := g.ModelRouting[requestedModel]; ok && len(providerIDs) > 0 {
		return providerIDs
	}

	// Try trailing-wildcard patterns after exact matching.
	for pattern, providerIDs := range g.ModelRouting {
		if MatchModelPattern(pattern, requestedModel) && len(providerIDs) > 0 {
			return providerIDs
		}
	}

	return nil
}

// MatchModelPattern checks an exact ID or a trailing-wildcard pattern.
// For example, "claude-opus-*" matches "claude-opus-4-20250514".
func MatchModelPattern(pattern, model string) bool {
	if pattern == model {
		return true
	}

	// A trailing asterisk matches any suffix.
	if before, ok := strings.CutSuffix(pattern, "*"); ok {
		prefix := before
		return strings.HasPrefix(model, prefix)
	}

	return false
}

// CloneGroup copies a group and its nested configuration.
func CloneGroup(g *Group) *Group {
	return (*Group)(accessview.CloneGroupConfig((*accessview.GroupConfig)(g)))
}

// GroupAllowsResponsesImages allows nil groups and configured image policies; otherwise it reads
// AllowImageGeneration.
func GroupAllowsResponsesImages(group *Group) bool {
	return group == nil || group.ResponsesImagePolicy != "" || group.AllowImageGeneration
}
