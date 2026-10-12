package routing

import (
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Group acceleration uses one policy so Fast, Ultra Fast, and force-off settings cannot conflict.
const (
	GroupOpenAIFastPolicyFollowRequest  = "follow_request"
	GroupOpenAIFastPolicyForcePriority  = "force_priority"
	GroupOpenAIFastPolicyForceUltrafast = "force_ultrafast"
	GroupOpenAIFastPolicyForceOff       = "force_off"
)

// EffectiveOpenAIFastPolicy prefers a named policy and otherwise uses the legacy boolean.
// @project-doc docs/interfaces/openai_upstream.md#openai_fast_policy
func (g *Group) EffectiveOpenAIFastPolicy() string {
	if g == nil {
		return GroupOpenAIFastPolicyFollowRequest
	}
	policy, err := ResolveGroupOpenAIFastPolicyInput(&g.OpenAIFastPolicy, g.ForceOpenAIFast)
	if err != nil {
		return GroupOpenAIFastPolicyFollowRequest
	}
	return policy
}

// ResolveGroupOpenAIFastPolicyInput validates named policies and interprets legacy input.
func ResolveGroupOpenAIFastPolicyInput(raw *string, legacyForce bool) (string, error) {
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
