package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// credentialBudgetKey stores the request's shared credential budget for platform preparation.
const credentialBudgetKey = "grok_credential_failover_deadline"

// CredentialObserver records credential failure classifications in Ops.
type CredentialObserver struct{ Context *gin.Context }

// RequestCredentialExecutor binds shared credential resolution to media, WS, and text HTTP requests.
type RequestCredentialExecutor struct {
	Runtime *gatewayadapter.RequestCredentials
}

// RequestCredentialBudget returns the request's shared budget; consumers start its timer.
func RequestCredentialBudget(c *gin.Context) *requeststate.CredentialBudget {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(credentialBudgetKey); ok {
		if state, ok := value.(*requeststate.CredentialBudget); ok {
			return state
		}
	}
	state := &requeststate.CredentialBudget{}
	c.Set(credentialBudgetKey, state)
	return state
}

func (o CredentialObserver) ObserveCredentialFailure(id int64, class forward.GrokCredentialFailure) {
	AppendOpsUpstreamError(o.Context, ops.OpsUpstreamErrorEvent{
		Platform:   capability.PlatformGrok,
		ProviderID: id,
		Stage:      string(forward.GatewayFailureStageProviderAuth),
		Scope:      string(class.Scope),
		Reason:     string(class.Reason),
		Kind:       "credential_failover",
		Message:    class.Message,
	})
}

func (s *RequestCredentialExecutor) Resolve(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider) (string, string, error) {
	return s.Runtime.Resolve(ctx, RequestCredentialBudget(c), CredentialObserver{Context: c}, target)
}
