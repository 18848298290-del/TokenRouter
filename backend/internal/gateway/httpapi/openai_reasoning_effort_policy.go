package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Group effort limits apply only when the selected Ops platform matches the
// policy platform. A group can still have an applicable policy after its
// invalid maximum or unexpressible mappings have been discarded, so
// applicability is distinct from having a usable limit or mapping.
//
// Requested effort is bound before group rewriting so result attribution can
// distinguish the client control from the forwarded value. Messages-to-OpenAI
// conversion binds policy in the request context only for a nonblank string
// output_config.effort; when that field is absent, the bridge's own default
// remains unbound by this step. The requeststate helpers implement the later
// model-sensitive mapping and cap rules.

// ReasoningEffortPolicyForRequest returns the selected group's usable effort
// policy for the supplied platform. A missing group or different selected Ops
// platform returns false applicability. For an applicable group, an invalid
// maximum becomes empty and mappings with invalid endpoints are omitted.
//
// Those filtering results do not turn an applicable group into an absent
// policy: the final boolean remains true and the over-limit action is
// retained. Callers therefore use applicability to decide whether to invoke
// policy processing, while the returned maximum and mappings describe which
// controls remain expressible.
func ReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey, platform string) (string, []routing.ReasoningEffortMapping, string, bool) {
	if apiKey == nil || apiKey.Group == nil || selectedOpsPlatform(c) != platform {
		return "", nil, "", false
	}
	group := apiKey.Group
	maxEffort, err := routing.NormalizeMaxReasoningEffortForPlatform(platform, group.MaxReasoningEffort)
	// A maximum unsupported by this platform cannot constrain forwarding; keep
	// the group applicable so its other usable mappings and action remain available.
	if err != nil {
		maxEffort = ""
	}
	// Both ends need platform-supported values before a mapping can run; discard
	// invalid pairs while retaining the order of the remaining group mappings.
	var mappings []routing.ReasoningEffortMapping
	for _, mapping := range group.ReasoningEffortMappings {
		if _, err := routing.NormalizeReasoningEffortMappingValueForPlatform(platform, mapping.From); err != nil {
			continue
		}
		if _, err := routing.NormalizeReasoningEffortMappingValueForPlatform(platform, mapping.To); err != nil {
			continue
		}
		mappings = append(mappings, mapping)
	}
	return maxEffort, mappings, group.MaxReasoningEffortOverLimit, true
}

// OpenAIReasoningEffortPolicyForRequest returns the current group's usable
// OpenAI effort controls and applicability. Callers check that flag even when
// maximum and mappings are empty, because filtering invalid controls retains
// an applicable group policy.
func OpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey) (string, []routing.ReasoningEffortMapping, string, bool) {
	return ReasoningEffortPolicyForRequest(c, apiKey, capability.PlatformOpenAI)
}

// AnthropicReasoningEffortPolicyForRequest returns the selected group's usable
// Anthropic effort controls and applicability. A missing group or different
// selected Ops platform returns false; an applicable group retains its flag
// after invalid controls are filtered.
func AnthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey) (string, []routing.ReasoningEffortMapping, string, bool) {
	return ReasoningEffortPolicyForRequest(c, apiKey, capability.PlatformAnthropic)
}

// ApplyOpenAIReasoningEffortPolicyForRequest binds the client effort before
// applying the selected OpenAI group policy. An inapplicable policy returns
// the supplied bytes with false change; mapping and limit errors from
// requeststate reach the HTTP entry for rejection.
func ApplyOpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey, body []byte) ([]byte, bool, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	BindRequestedReasoningEffort(c, body, model)
	maxEffort, mappings, overLimit, ok := OpenAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return requeststate.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

// ApplyAnthropicReasoningEffortPolicyForRequest binds requested effort and
// applies the selected Anthropic group policy. The shared requeststate
// processor supports output_config.effort and reasoning.effort; an
// inapplicable policy returns the input unchanged, and processing errors reach
// the caller.
func ApplyAnthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey, body []byte) ([]byte, bool, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	BindRequestedReasoningEffort(c, body, model)
	maxEffort, mappings, overLimit, ok := AnthropicReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return requeststate.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

// BindOpenAIReasoningEffortPolicyForMessagesRequest binds requested effort and
// optionally installs OpenAI policy for later Messages conversion. Missing
// HTTP request state ends the operation, and a missing, nonstring or blank
// output_config.effort leaves conversion policy unbound.
//
// For a supplied effort and applicable OpenAI group, the new request context
// carries maximum, mappings, action and current model together. Conversion can
// then apply that client policy after producing Responses fields. This
// distinction keeps the bridge's generated default separate from a client
// effort that needs group enforcement.
func BindOpenAIReasoningEffortPolicyForMessagesRequest(c *gin.Context, apiKey *apikey.APIKey, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	BindRequestedReasoningEffort(c, body, model)
	// The bridge can introduce its own default effort during conversion. Bind
	// group enforcement here only when the client supplied a usable effort string.
	effort := gjson.GetBytes(body, "output_config.effort")
	if !effort.Exists() || effort.Type != gjson.String || strings.TrimSpace(effort.String()) == "" {
		return
	}
	maxEffort, mappings, overLimit, ok := OpenAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return
	}
	ctx := requeststate.WithOpenAIReasoningEffortPolicyForModel(
		c.Request.Context(),
		maxEffort,
		mappings,
		overLimit,
		model,
	)
	c.Request = c.Request.WithContext(ctx)
}
