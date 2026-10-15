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

// request_credentials.go binds credential resolution to a shared HTTP request
// budget and records classified authentication failures.
// Grok credential recovery shares one time budget across attempts belonging to
// an HTTP request. Gin stores that budget for text, media and WebSocket
// adapters, and classified credential failures are associated with the request
// as provider-auth Ops events.

// credentialBudgetKey names the Gin value shared by credential recovery attempts
// so each provider lookup can use the same request-local timing state.
const credentialBudgetKey = "grok_credential_failover_deadline"

// CredentialObserver associates classified credential failures with the request Ops history.
type CredentialObserver struct {
	Context *gin.Context // request Ops association
}

// RequestCredentialExecutor binds shared credential resolution to media, WS, and text HTTP requests.
type RequestCredentialExecutor struct {
	Runtime *gatewayadapter.RequestCredentials // application credential lookup runtime
}

// RequestCredentialBudget returns the request-shared credential budget,
// creating and storing it when absent or mistyped. A nil Gin context or stored
// typed nil returns nil. Credential resolution starts the timer when acquiring
// this budget, so obtaining the state alone does not spend it.
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

// ObserveCredentialFailure appends a Grok authentication-stage Ops event using
// the supplied failure classification. The caller provides the sanitized
// message, scope and reason; this adapter associates them with its request
// context and provider ID.
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

// Resolve delegates credential lookup with the request budget and an Ops
// observer. Callers supply a bound runtime and execution target; Grok recovery
// shares its deadline across provider attempts while other credentials use the
// runtime source.
func (s *RequestCredentialExecutor) Resolve(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider) (string, string, error) {
	return s.Runtime.Resolve(ctx, RequestCredentialBudget(c), CredentialObserver{Context: c}, target)
}
