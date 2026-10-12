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

// ReasoningEffortPolicyForRequest returns the group policy for the selected platform.
// Authentication has already selected the group for a composite key.
func ReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey, platform string) (string, []routing.ReasoningEffortMapping, string, bool) {
	if apiKey == nil || apiKey.Group == nil || selectedOpsPlatform(c) != platform {
		return "", nil, "", false
	}
	group := apiKey.Group
	maxEffort, err := routing.NormalizeMaxReasoningEffortForPlatform(platform, group.MaxReasoningEffort)
	if err != nil {
		maxEffort = ""
	}
	// Discard mappings whose effort values cannot be expressed by the selected platform.
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

// OpenAIReasoningEffortPolicyForRequest returns the OpenAI group policy.
func OpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey) (string, []routing.ReasoningEffortMapping, string, bool) {
	return ReasoningEffortPolicyForRequest(c, apiKey, capability.PlatformOpenAI)
}

// AnthropicReasoningEffortPolicyForRequest returns the Anthropic group policy.
func AnthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey) (string, []routing.ReasoningEffortMapping, string, bool) {
	return ReasoningEffortPolicyForRequest(c, apiKey, capability.PlatformAnthropic)
}

// ApplyOpenAIReasoningEffortPolicyForRequest records the client effort before applying group policy.
func ApplyOpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey, body []byte) ([]byte, bool, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	BindRequestedReasoningEffort(c, body, model)
	maxEffort, mappings, overLimit, ok := OpenAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return requeststate.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

// ApplyAnthropicReasoningEffortPolicyForRequest applies group policy before forwarding Messages or Responses.
// It supports output_config.effort and reasoning.effort.
func ApplyAnthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *apikey.APIKey, body []byte) ([]byte, bool, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	BindRequestedReasoningEffort(c, body, model)
	maxEffort, mappings, overLimit, ok := AnthropicReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return requeststate.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

// BindOpenAIReasoningEffortPolicyForMessagesRequest binds policy for a client-supplied output_config.effort.
// When the field is absent, the bridge-supplied medium remains a default and no policy is bound.
func BindOpenAIReasoningEffortPolicyForMessagesRequest(c *gin.Context, apiKey *apikey.APIKey, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	BindRequestedReasoningEffort(c, body, model)
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
