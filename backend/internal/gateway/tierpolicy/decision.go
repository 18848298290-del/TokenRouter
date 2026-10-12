package tierpolicy

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// DecisionInput supplies platform and group policy, with lazy policy and pricing checks.
type DecisionInput struct {
	Model            string
	GroupPolicy      string
	OpenAI           bool
	Evaluate         func(string) (string, string)
	KeyPolicy        func() string
	ForceOnSupported func() bool
}

// Decision describes the service tier to write, remove, or reject.
type Decision struct {
	Tier        string
	DeleteField bool
	Blocked     *BlockedError
}

// IsAcceleratedTier reports whether the tier is Fast or Ultra Fast.
func IsAcceleratedTier(tier string) bool {
	return tier == OpenAIFastTierPriority || tier == OpenAIFastTierUltrafast
}

// Resolve applies group, system, and API key service tier policies.
// System rules run before key overrides and again after a key changes the tier, so filter/block also covers force_on.
func Resolve(input DecisionInput, rawTier string, hasField bool) Decision {
	normTier := openai.ServiceTierValue(rawTier)
	groupPolicy := input.GroupPolicy
	switch groupPolicy {
	case routing.GroupOpenAIFastPolicyForcePriority:
		normTier, hasField = OpenAIFastTierPriority, true
	case routing.GroupOpenAIFastPolicyForceUltrafast:
		normTier, hasField = OpenAIFastTierUltrafast, true
	}
	applySystemAction := func(tier string) (Decision, bool) {
		action, errMsg := input.Evaluate(tier)
		switch action {
		case "block":
			if errMsg == "" {
				errMsg = fmt.Sprintf("openai service_tier=%s is not allowed for model %s", tier, input.Model)
			}
			return Decision{Blocked: &BlockedError{Message: errMsg}}, true
		case "filter":
			return Decision{DeleteField: true}, true
		case OpenAIFastPolicyActionForcePriority:
			return Decision{Tier: OpenAIFastTierPriority}, true
		case OpenAIFastPolicyActionForceUltrafast:
			return Decision{Tier: OpenAIFastTierUltrafast}, true
		default:
			return Decision{}, false
		}
	}

	// A system action other than pass takes precedence over key policy.
	if normTier != "" {
		if decision, handled := applySystemAction(normTier); handled {
			return decision
		}
	}

	// After system evaluation, a group force_off prevents a key from enabling Fast.
	if groupPolicy == routing.GroupOpenAIFastPolicyForceOff {
		if IsAcceleratedTier(normTier) {
			return Decision{DeleteField: hasField}
		}
		return Decision{Tier: normTier}
	}
	candidateTier := normTier
	policy := input.KeyPolicy()
	keyPolicyApplicable := false
	switch policy {
	case apikey.APIKeyFastModePolicyForceOn:
		keyPolicyApplicable = input.ForceOnSupported()
	case apikey.APIKeyFastModePolicyForceOff:
		// Key force_off applies to OpenAI accelerated tiers.
		keyPolicyApplicable = input.OpenAI
	}
	candidateChanged := false
	if keyPolicyApplicable {
		switch policy {
		case apikey.APIKeyFastModePolicyForceOn:
			// Enabling Fast on a key must not downgrade a group-forced Ultra Fast tier.
			if groupPolicy != routing.GroupOpenAIFastPolicyForceUltrafast {
				candidateTier = OpenAIFastTierPriority
			}
		case apikey.APIKeyFastModePolicyForceOff:
			// Keep non-accelerated tiers such as flex, auto, default, and scale.
			if IsAcceleratedTier(normTier) {
				candidateTier = ""
			}
		}
		candidateChanged = candidateTier != normTier
	}

	// Recheck system policy after a key injects or changes the tier.
	if candidateTier != "" {
		if candidateChanged {
			if decision, handled := applySystemAction(candidateTier); handled {
				return decision
			}
		}
		return Decision{Tier: candidateTier}
	}
	if policy == apikey.APIKeyFastModePolicyForceOff && keyPolicyApplicable && IsAcceleratedTier(normTier) {
		return Decision{DeleteField: hasField}
	}
	return Decision{}
}

// ApplyBody applies group, system, and key Fast policies and normalizes service_tier.
// Chat and Messages normalize earlier; passthrough and Responses rely on this step.
// The fast alias must become priority to avoid upstream rejection.
func ApplyBody(body []byte, input DecisionInput) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	tierResult := gjson.GetBytes(body, "service_tier")
	decision := Resolve(input, tierResult.String(), tierResult.Exists())
	if decision.Blocked != nil {
		return body, decision.Blocked
	}
	if decision.DeleteField {
		trimmed, err := sjson.DeleteBytes(body, "service_tier")
		if err != nil {
			return body, fmt.Errorf("strip service_tier from body: %w", err)
		}
		return trimmed, nil
	}
	if decision.Tier != "" && (!tierResult.Exists() || decision.Tier != tierResult.String()) {
		updated, err := sjson.SetBytes(body, "service_tier", decision.Tier)
		if err != nil {
			return body, fmt.Errorf("apply service_tier to body: %w", err)
		}
		return updated, nil
	}
	return body, nil
}
