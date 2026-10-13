package routing

import (
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Group acceleration stores one named policy for request-controlled, forced, or
// disabled service tiers. Legacy ForceOpenAIFast input is interpreted only when
// the named input is absent or blank, so administrative writes and request
// snapshots resolve the same compatibility values.

// Group acceleration values govern request-tier handling and provide one stored
// choice for both named input and legacy forcing.
const (
	GroupOpenAIFastPolicyFollowRequest  = "follow_request"  // use the request's tier
	GroupOpenAIFastPolicyForcePriority  = "force_priority"  // request priority service
	GroupOpenAIFastPolicyForceUltrafast = "force_ultrafast" // request fast service
	GroupOpenAIFastPolicyForceOff       = "force_off"       // remove accelerated tiers
)

// EffectiveOpenAIFastPolicy returns the named group acceleration policy, using
// ForceOpenAIFast for blank legacy records. A nil group or an invalid saved
// policy yields follow_request; request executors determine whether the
// selected provider supports the resulting tier.
// @project-doc docs/interfaces/openai_upstream.md#openai_fast_policy
func (g *Group) EffectiveOpenAIFastPolicy() string {
	if g == nil {
		return GroupOpenAIFastPolicyFollowRequest
	}
	policy, err := ResolveGroupOpenAIFastPolicyInput(&g.OpenAIFastPolicy, g.ForceOpenAIFast)
	// Malformed saved policies use request-controlled acceleration so reads can
	// proceed; administrative input validation still returns the policy error to
	// its caller.
	if err != nil {
		return GroupOpenAIFastPolicyFollowRequest
	}
	return policy
}

// ResolveGroupOpenAIFastPolicyInput validates a supplied policy and resolves
// blank input from the legacy boolean. Values are trimmed but case-sensitive;
// legacy true selects force_priority, and legacy false selects follow_request.
func ResolveGroupOpenAIFastPolicyInput(raw *string, legacyForce bool) (string, error) {
	// Older inputs express acceleration as a boolean. Translate that input when
	// the named choice is absent so a supplied named policy takes precedence.
	if raw == nil || strings.TrimSpace(*raw) == "" {
		if legacyForce {
			return GroupOpenAIFastPolicyForcePriority, nil
		}
		return GroupOpenAIFastPolicyFollowRequest, nil
	}
	policy := strings.TrimSpace(*raw)
	switch policy {
	case GroupOpenAIFastPolicyFollowRequest, GroupOpenAIFastPolicyForcePriority, GroupOpenAIFastPolicyForceUltrafast, GroupOpenAIFastPolicyForceOff:
		return policy, nil
	default:
		return "", infraerrors.Newf(infraerrors.Category(400), "INVALID_OPENAI_FAST_POLICY", "invalid openai_fast_policy: %q", policy)
	}
}
