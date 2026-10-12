package requeststate

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Group reasoning policy maps an explicitly supplied client effort before
// applying its ceiling. The request model selects scoped mappings, so capture
// can retain the client's pre-policy choice while outgoing fields carry the
// mapped or capped choice.
//
// Each supported effort spelling is processed independently and missing fields
// remain omitted. A deny decision returns the original body even when earlier
// fields have produced temporary replacements, giving the caller one rejection
// result instead of a partially adopted policy rewrite. Individual byte-edit
// failures are skipped and other fields can still change. Context binding
// copies mapping entries and retains the client model for later attempts, while
// the body model supplies that scope when no model was captured.

// requestedReasoningEffortContextKey identifies the client choice captured
// before policy rewriting.
type requestedReasoningEffortContextKey struct{}

// openAIReasoningEffortPolicyContextKey identifies the group policy captured
// for request rewriting.
type openAIReasoningEffortPolicyContextKey struct{}

// openAIReasoningEffortPolicy captures the group ceiling, overflow action,
// client model, and copied effort mappings. Context readers apply these values
// later without reading a mutable caller mapping slice.
type openAIReasoningEffortPolicy struct {
	// mappings selects one rewrite using requestModel, then maxEffort and overLimit
	// govern ceiling handling. Binding copies mapping entries, whose current
	// representation contains string values rather than mutable nested collections.
	maxEffort    string                           // configured group effort ceiling
	overLimit    string                           // configured ceiling overflow action
	requestModel string                           // trimmed captured client model
	mappings     []routing.ReasoningEffortMapping // copied model-scoped effort mappings
}

// WithRequestedReasoningEffort stores a trimmed pre-policy choice in a derived
// context, using Background for nil. Blank input returns the parent without
// clearing previous evidence; this setter retains spelling rather than
// canonicalizing it.
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

// RequestedReasoningEffortFromContext returns a separate pointer to the trimmed
// captured client choice. Missing, nil, wrong-type and blank evidence yields
// nil, and changing the returned string pointer leaves the context value
// intact.
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

// WithOpenAIReasoningEffortPolicyForModel binds policy and its client-model
// scope in a derived context. Nil uses Background, the model is trimmed, and
// mapping entries are copied; effort labels and actions are interpreted when
// policy runs.
func WithOpenAIReasoningEffortPolicyForModel(ctx context.Context, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit, requestModel string) context.Context {
	return withOpenAIReasoningEffortPolicyForModel(ctx, maxEffort, mappings, overLimit, requestModel)
}

// withOpenAIReasoningEffortPolicyForModel constructs the captured policy using
// a copied mapping slice. It tolerates nil ctx through Background and retains
// raw ceiling/action spellings for their later normalization, while trimming
// requestModel.
func withOpenAIReasoningEffortPolicyForModel(ctx context.Context, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit, requestModel string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	// Capture mapping entries before later attempts use this policy so edits to
	// the caller's slice cannot redirect that request's stored model-scoped effort rules.
	return context.WithValue(ctx, openAIReasoningEffortPolicyContextKey{}, openAIReasoningEffortPolicy{
		maxEffort:    maxEffort,
		overLimit:    overLimit,
		requestModel: strings.TrimSpace(requestModel),
		mappings:     append([]routing.ReasoningEffortMapping(nil), mappings...),
	})
}

// ApplyOpenAIReasoningEffortPolicyFromContext applies captured policy to the
// supplied body and reports its rewrite result. Missing or nil context returns
// the same slice with false and no error; a bound policy retains its captured
// model scope.
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

// ApplyOpenAIReasoningEffortPolicy maps explicit effort before capping or
// rejecting it using the body's model scope. Missing fields remain omitted,
// individual byte-edit failures are skipped, and denial returns the supplied
// body with false and an error.
func ApplyOpenAIReasoningEffortPolicy(body []byte, maxEffort string, mappings []routing.ReasoningEffortMapping, overLimit string) ([]byte, bool, error) {
	return ApplyOpenAIReasoningEffortPolicyForModel(body, maxEffort, mappings, overLimit, "")
}

// ApplyOpenAIReasoningEffortPolicyForModel maps supported explicit field
// spellings and then applies a recognized group ceiling. Nonblank requestModel
// supplies mapping scope; otherwise the current body model is used, and absent
// or nonstring effort fields stay unchanged.
//
// Each field receives one scoped mapping before rank normalization and ceiling
// handling. A deny result returns the original body and false even after
// earlier temporary field updates, so callers do not adopt a partial rewrite
// from a rejected request. Individual sjson failures are ignored and later
// fields continue, allowing successful edits to be returned together. Omitted
// effort is never synthesized, and an unrecognized ceiling without mappings
// leaves the body untouched.
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

		// Mapping uses the client-model scope before ceiling comparison, so an
		// allowed mapped level can proceed even when the supplied level exceeded the ceiling.
		effective, _ := routing.MapReasoningEffort(original, mappings, requestModel)
		if currentRank, recognized := routing.ReasoningEffortRank(effective); recognized {
			effective = routing.NormalizeMaxReasoningEffort(effective)
			if hasMax && currentRank > maxRank {
				// Earlier successful field edits are still temporary here. Return the
				// supplied bytes on denial so the caller cannot adopt a partially rejected policy result.
				if deny {
					return body, false, &routing.ReasoningEffortOverLimitError{Requested: effective, Max: canonicalMax}
				}
				// Downgrade applies the captured ceiling after model-scoped mapping,
				// reducing this explicit field while other outgoing spellings remain independently processed.
				effective = canonicalMax
			}
		}
		if effective == original {
			continue
		}

		updated, err := sjson.SetBytes(result, path, effective)
		if err != nil {
			// This field's byte edit failed; retain the preceding bytes and continue
			// processing other explicit spellings under the same captured policy.
			continue
		}
		result = updated
		changed = true
	}
	return result, changed, nil
}
