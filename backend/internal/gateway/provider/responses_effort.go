package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// OpenAICompatAnthropicReasoningEffort 按最终属性选择 max 或兼容档位。
// 缺少属性时保留客户端明确指定的值。
func OpenAICompatAnthropicReasoningEffort(req *protocolanthropic.AnthropicRequest, upstreamModel, convertedEffort string, attributes ...modelcatalog.Attributes) string {
	if req == nil || req.OutputConfig == nil || !strings.EqualFold(strings.TrimSpace(req.OutputConfig.Effort), "max") {
		return convertedEffort
	}
	var attrs modelcatalog.Attributes
	if len(attributes) > 0 {
		attrs = attributes[0]
	}
	if supported := attrs.EffortSupport("max"); supported == nil || *supported {
		return "max"
	}
	if strings.EqualFold(strings.TrimSpace(convertedEffort), "max") {
		return "xhigh"
	}
	return convertedEffort
}
