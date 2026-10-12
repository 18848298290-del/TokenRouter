package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// OpenAICountTarget supplies the selected provider snapshot and token-count execution.
type OpenAICountTarget interface {
	Snapshot() provider.ProviderSnapshot
	ForwardCount(context.Context, *gin.Context, []byte, string) error
}

// InputTokensTarget performs native preflight with a per-attempt retry budget.
type InputTokensTarget interface {
	Snapshot() provider.ProviderSnapshot
	RetryLimit() int
	ForwardInputTokens(context.Context, *gin.Context, []byte) error
}

type InputTokensSelection struct {
	Target  InputTokensTarget
	Release func()
}

// OpenAITokenExecution supplies token-count execution capabilities without funding writes or completion
// submission.
type OpenAITokenExecution interface {
	PlanTokenRoute(context.Context, *apikey.APIKey, string) routing.RoutePlan
	TokenSessionHash(*gin.Context, []byte) string
	SelectCount(context.Context, *int64, string, string, string) (OpenAICountTarget, error)
	SelectInputTokens(context.Context, *int64, string, string, string, map[int64]struct{}, string) (InputTokensSelection, error)
}

type TokenFunding interface {
	CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
}

// OpenAITokenPorts binds shared readers and services; each request creates its own attempt state.
type OpenAITokenPorts struct {
	Diagnoser           routing.ModelAvailabilityDiagnoser
	ResolvedDiagnoser   routing.ModelAvailabilityDiagnoser
	Execution           OpenAITokenExecution
	Funding             TokenFunding
	MissingDependencies []string
	Rules               ErrorRuleMatcher
}

func (p OpenAITokenPorts) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	return apikey.CopyAPIKey(key), ok
}

func (p OpenAITokenPorts) Dependencies(c *gin.Context, log *zap.Logger) bool {
	if len(p.MissingDependencies) == 0 {
		return true
	}
	if log == nil {
		log = RequestLogger(c, "handler.openai_gateway.responses")
	}
	log.Error("openai.handler_dependencies_missing", zap.Strings("missing_dependencies", p.MissingDependencies))
	if c != nil && c.Writer != nil && !c.Writer.Written() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Service temporarily unavailable"}})
	}
	return false
}

func (OpenAITokenPorts) AllowsMessages(key *apikey.APIKey) bool {
	key = apikey.CopyAPIKey(key)
	return key == nil || key.Group == nil || key.Group.AllowsClientProtocol(protocol.ProtocolAnthropicMessages)
}

func (OpenAITokenPorts) PolicyDenied(c *gin.Context) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

func (OpenAITokenPorts) ObserveRequest(c *gin.Context, model string, stream bool) {
	SetOpsRequestContext(c, model, stream)
}

func (OpenAITokenPorts) ObserveEndpoint(c *gin.Context, stream bool) {
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
}

func (p OpenAITokenPorts) Plan(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	return p.Execution.PlanTokenRoute(ctx, apikey.CopyAPIKey(key), model)
}

func (OpenAITokenPorts) BindPlan(c *gin.Context, plan routing.RoutePlan) {
	c.Request = c.Request.WithContext(requeststate.WithRoutePlan(c.Request.Context(), plan))
}

func (OpenAITokenPorts) MessageProviderModel(ctx context.Context, key *apikey.APIKey, model string) string {
	return ResolveOpenAIMessagesProviderLayerModelForRequest(ctx, model)
}

func (OpenAITokenPorts) MappedBodyCache(body []byte) func(bool, string) []byte {
	return requeststate.NewModelMappedBodyCache(body, protocolopenai.ReplaceModelInBody)
}

func (p OpenAITokenPorts) Eligibility(ctx context.Context, key *apikey.APIKey, sub *billing.UserSubscription) error {
	keyCopy := apikey.CopyAPIKey(key)
	return p.Funding.CheckKey(ctx, keyCopy, sub, "", false)
}

func (OpenAITokenPorts) Platform(key *apikey.APIKey) string {
	return ""
}

func (p OpenAITokenPorts) SessionHash(c *gin.Context, _ OpenAISessionInput, body []byte) string {
	return p.Execution.TokenSessionHash(c, body)
}

func (OpenAITokenPorts) AuthLatency(c *gin.Context, ms int64) {
	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, ms)
}

// tokenSelectionError distinguishes unsupported models from capacity exhaustion.
func tokenSelectionError(c *gin.Context, diagnose routing.ModelAvailabilityDiagnoser, key *apikey.APIKey, routingModel, displayModel string) SelectionErrorResponse {
	var group *int64
	if key != nil {
		group = key.GroupID
	}
	result := ClassifySelectionError(c.Request.Context(), diagnose, group, routingModel, displayModel, "")
	if result.ModelNotFound {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalModelConfiguration)
	}
	return result
}

// OpenAICompatibleSelectionErrorForLog replaces OpenAI provider labels with Grok labels for Grok requests.
func OpenAICompatibleSelectionErrorForLog(err error, platform string) error {
	if err == nil || platform != "grok" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), "OpenAI providers", "Grok providers")
	if message == err.Error() {
		return err
	}
	return fmt.Errorf("%s", message)
}
