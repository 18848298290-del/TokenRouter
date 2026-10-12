package requeststate

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type requestedReasoningEffortContextKey struct{}

type openAIReasoningEffortPolicyContextKey struct{}

// openAIReasoningEffortPolicy holds request policy with a copied group mapping slice.
type openAIReasoningEffortPolicy struct {
	maxEffort    string
	overLimit    string
	requestModel string
	mappings     []routing.ReasoningEffortMapping
}

// WithRequestedReasoningEffort binds the client effort captured before policy rewriting.
func WithRequestedReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedReasoningEffortContextKey{}, effort)
}

// RequestedReasoningEffortFromContext returns the captured client effort, or nil when absent.
func RequestedReasoningEffortFromContext(ctx context.Context) *string {
	if ctx == nil {
		return nil
	}
	effort, ok := ctx.Value(requestedReasoningEffortContextKey{}).(string)
	if !ok {
		return nil
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	return &effort
}

// WithOpenAIReasoningEffortPolicyForModel binds policy and the client model for scoped mappings.
func WithOpenAIReasoningEffortPolicyForModel(ctx context.Context, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit, requestModel string) context.Context {
	return withOpenAIReasoningEffortPolicyForModel(ctx, maxEffort, mappings, overLimit, requestModel)
}

func withOpenAIReasoningEffortPolicyForModel(ctx context.Context, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit, requestModel string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIReasoningEffortPolicyContextKey{}, openAIReasoningEffortPolicy{
		maxEffort:    maxEffort,
		overLimit:    overLimit,
		requestModel: strings.TrimSpace(requestModel),
		mappings:     append([]routing.ReasoningEffortMapping(nil), mappings...),
	})
}

// ApplyOpenAIReasoningEffortPolicyFromContext applies the bound request policy.
func ApplyOpenAIReasoningEffortPolicyFromContext(ctx context.Context, body []byte) ([]byte, bool, error) {
	if ctx == nil {
		return body, false, nil
	}
	policy, ok := ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
	if !ok {
		return body, false, nil
	}
	return ApplyOpenAIReasoningEffortPolicyForModel(body, policy.maxEffort, policy.mappings, policy.overLimit, policy.requestModel)
}

// ApplyOpenAIReasoningEffortPolicy maps effort for the request model before capping or rejecting excess effort.
// Omitted values stay absent so upstream defaults apply.
func ApplyOpenAIReasoningEffortPolicy(body []byte, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit string) ([]byte, bool, error) {
	return ApplyOpenAIReasoningEffortPolicyForModel(body, maxEffort, mappings, overLimit, "")
}

func ApplyOpenAIReasoningEffortPolicyForModel(body []byte, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit, requestModel string) ([]byte, bool, error) {
	maxRank, hasMax := routing.ReasoningEffortRank(maxEffort)
	if len(body) == 0 || (!hasMax && len(mappings) == 0) {
		return body, false, nil
	}

	if strings.TrimSpace(requestModel) == "" {
		requestModel = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	deny := hasMax && routing.NormalizeMaxReasoningEffortOverLimit(overLimit) == routing.ReasoningEffortOverLimitDeny
	canonicalMax := routing.NormalizeMaxReasoningEffort(maxEffort)
	result := body
	changed := false
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		field := gjson.GetBytes(result, path)
		if !field.Exists() || field.Type != gjson.String {
			continue
		}
		original := strings.TrimSpace(field.String())
		if original == "" {
			continue
		}

		effective, _ := routing.MapReasoningEffort(original, mappings, requestModel)
		if currentRank, recognized := routing.ReasoningEffortRank(effective); recognized {
			effective = routing.NormalizeMaxReasoningEffort(effective)
			if hasMax && currentRank > maxRank {
				if deny {
					return body, false, &routing.ReasoningEffortOverLimitError{Requested: effective, Max: canonicalMax}
				}
				effective = canonicalMax
			}
		}
		if effective == original {
			continue
		}

		updated, err := sjson.SetBytes(result, path, effective)
		if err != nil {
			continue
		}
		result = updated
		changed = true
	}
	return result, changed, nil
}
