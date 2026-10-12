package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// opsCyberPolicyKey stores upstream refusal evidence for an HTTP request or WebSocket turn.
const opsCyberPolicyKey = "ops_cyber_policy"

// MarkOpsCyberPolicy keeps the first marker so later events cannot overwrite its usage or failure state.
func MarkOpsCyberPolicy(c *gin.Context, mark moderationflow.Mark) {
	if c == nil || GetOpsCyberPolicy(c) != nil {
		return
	}
	mark.Code = "cyber_policy"
	mark.Message = strings.TrimSpace(mark.Message)
	mark.Body = strings.TrimSpace(mark.Body)
	c.Set(opsCyberPolicyKey, &mark)
}

// GetOpsCyberPolicy returns the current request or turn's recorded marker, or nil if absent.
func GetOpsCyberPolicy(c *gin.Context) *moderationflow.Mark {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(opsCyberPolicyKey); ok {
		if mark, ok := value.(*moderationflow.Mark); ok && mark != nil {
			return mark
		}
	}
	return nil
}

// ClearOpsCyberPolicy stores a typed nil so the next WebSocket turn can record fresh evidence.
func ClearOpsCyberPolicy(c *gin.Context) {
	if c != nil {
		c.Set(opsCyberPolicyKey, (*moderationflow.Mark)(nil))
	}
}

// MarkOpenAICyberPolicyEvent parses a provider event and records its refusal evidence in the HTTP context.
func MarkOpenAICyberPolicyEvent(c *gin.Context, payload []byte, upstreamStatus int, usage *openai.ForwardUsage) bool {
	mark := gatewayadapter.ParseOpenAICyberPolicyEvent(payload, upstreamStatus, usage)
	if mark == nil {
		return false
	}
	MarkOpsCyberPolicy(c, *mark)
	return true
}
