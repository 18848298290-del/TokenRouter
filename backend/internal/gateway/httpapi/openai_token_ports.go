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

// openai_token_ports.go defines controlled token-count targets and binds
// counting admission services.
// Token counting exposes controlled provider targets separately from diagnostic
// snapshots. HTTP adapters bind an effective-key copy, route plan, funding
// reader, and request-local body cache, then use the dedicated count loops to
// execute auxiliary queries with their own error output.

// OpenAICountTarget binds a selected provider snapshot to one
// Messages-compatible count.
type OpenAICountTarget interface {
	// Snapshot returns selected-provider metadata for count observations
	// and error logs.
	Snapshot() provider.ProviderSnapshot
	// ForwardCount executes the selected target's Messages-compatible token
	// query. Callers supply mapped body bytes and the provider-layer model.
	// The target writes count or protocol error output and returns its
	// execution error.
	ForwardCount(context.Context, *gin.Context, []byte, string) error
}

// InputTokensTarget binds provider metadata and retry allowance to native input
// counting.
type InputTokensTarget interface {
	// Snapshot returns selected-provider metadata for count observations
	// and error logs.
	Snapshot() provider.ProviderSnapshot
	// RetryLimit returns the selected provider's configured same-provider
	// counting retry allowance.
	RetryLimit() int
	// ForwardInputTokens executes native Responses counting for the
	// selected target. The target writes count or error output and can
	// return a classified failover error. Its caller owns any separate
	// selection release callback.
	ForwardInputTokens(context.Context, *gin.Context, []byte) error
}

// InputTokensSelection carries a controlled count target and optional selection
// cleanup. The forwarding adapter retires Release before the count loop retries
// or switches.
type InputTokensSelection struct {
	Target InputTokensTarget // selected controlled counting target
	// Release retires any resource returned by selection. The counting
	// forward adapter invokes it on both successful and failed exchanges
	// before retry handling.
	Release func()
}

// OpenAITokenExecution supplies route planning and selection for auxiliary
// token counts. Selected targets carry execution authority; snapshots provide
// only request observations.
type OpenAITokenExecution interface {
	// PlanTokenRoute resolves the effective key and model into a counting
	// route. Callers bind its result before selection. Group mapping
	// retains the requested identity separately from the model used to
	// select and prepare upstream input.
	PlanTokenRoute(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// TokenSessionHash derives the counting request's session identity from
	// HTTP input. The result seeds compatible-provider selection and is
	// reused through any native preflight retries associated with the same
	// request.
	TokenSessionHash(*gin.Context, []byte) string
	// SelectCount selects a compatible count target using group, session,
	// model, and platform. This operation uses slot-free selection after
	// group admission. A target may accompany an error; callers
	// forward only when selection succeeds.
	SelectCount(context.Context, *int64, string, string, string) (OpenAICountTarget, error)
	// SelectInputTokens selects a native count target with an
	// excluded-provider set. Selection uses select-only scheduler context.
	// Any resource returned with a usable target is owned by the forwarding
	// adapter and released before another attempt.
	SelectInputTokens(context.Context, *int64, string, string, string, map[int64]struct{}, string) (InputTokensSelection, error)
}

// TokenFunding checks current funding eligibility for authenticated auxiliary
// counting input.
type TokenFunding interface {
	// CheckKey checks funding admission for the supplied key, subscription,
	// and request flags. Counting callers pass an empty model and false
	// flag, then format any returned admission error before selecting a
	// count target.
	CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
}

// OpenAITokenPorts binds shared readers to request-local token counting
// adapters.
type OpenAITokenPorts struct {
	Diagnoser         routing.ModelAvailabilityDiagnoser // compatibility-count model availability reader
	ResolvedDiagnoser routing.ModelAvailabilityDiagnoser // routed preflight model availability reader
	Execution         OpenAITokenExecution               // app-bound route and counting selection
	Funding           TokenFunding                       // auxiliary funding admission checker
	// MissingDependencies stores app assembly failures. A nonempty list
	// causes Dependencies to reject entry processing before any counting
	// operation is called.
	MissingDependencies []string
	Rules               ErrorRuleMatcher // terminal preflight error-rule matcher
}

// Access returns the effective key or a copy of the authenticated context key.
// EffectiveAPIKey supplies request-owned data when present. The availability
// boolean from the authentication lookup is retained when that path provides
// the key.
func (p OpenAITokenPorts) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	return apikey.CopyAPIKey(key), ok
}

// Dependencies reports whether app assembly recorded missing token-entry
// services. It logs the stored names, using the request logger when log is nil.
// A 503 is written only when the HTTP response writer exists and remains
// unwritten.
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

// AllowsMessages reports whether a copied key's group permits Messages protocol
// input. Missing key or group returns true for this adapter check. Earlier
// authentication and later entry validation establish whether the request can
// proceed.
func (OpenAITokenPorts) AllowsMessages(key *apikey.APIKey) bool {
	key = apikey.CopyAPIKey(key)
	return key == nil || key.Group == nil || key.Group.AllowsClientProtocol(protocol.ProtocolAnthropicMessages)
}

// PolicyDenied marks the token request as limited by local protocol policy.
func (OpenAITokenPorts) PolicyDenied(c *gin.Context) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

// ObserveRequest publishes token-entry model and stream values to request
// observations.
func (OpenAITokenPorts) ObserveRequest(c *gin.Context, model string, stream bool) {
	SetOpsRequestContext(c, model, stream)
}

// ObserveEndpoint publishes the legacy request type used for auxiliary token
// observations.
func (OpenAITokenPorts) ObserveEndpoint(c *gin.Context, stream bool) {
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
}

// Plan delegates token routing using a copied effective key and requested
// model. Callers configure Execution and bind the returned plan to their
// request before selection. The caller's key remains available for the funding
// check.
func (p OpenAITokenPorts) Plan(ctx context.Context, key *apikey.APIKey, model string) routing.RoutePlan {
	return p.Execution.PlanTokenRoute(ctx, apikey.CopyAPIKey(key), model)
}

// BindPlan replaces the request context with one carrying the supplied route
// plan. Callers provide a nonnil HTTP request. Selection and model preparation
// subsequently read this request's plan rather than shared authentication
// state.
func (OpenAITokenPorts) BindPlan(c *gin.Context, plan routing.RoutePlan) {
	c.Request = c.Request.WithContext(requeststate.WithRoutePlan(c.Request.Context(), plan))
}

// MessageProviderModel trims the group model and registers its model-trace
// stage. The key parameter is unused by this adapter. Callers keep the client
// model separately for diagnostics and pass this returned name to provider
// selection.
func (OpenAITokenPorts) MessageProviderModel(ctx context.Context, key *apikey.APIKey, model string) string {
	return ResolveOpenAIMessagesProviderLayerModelForRequest(ctx, model)
}

// MappedBodyCache creates a request-local cache of model-rewritten body bytes.
// Unmapped reads can return the supplied body, and mapped reads retain cached
// storage. Callers treat returned bytes as immutable for the cache's lifetime.
func (OpenAITokenPorts) MappedBodyCache(body []byte) func(bool, string) []byte {
	return requeststate.NewModelMappedBodyCache(body, protocolopenai.ReplaceModelInBody)
}

// Eligibility delegates funding admission with a copied key and the supplied
// subscription. It passes an empty model and false request flag. Funding errors
// are returned to the count entry for client formatting before provider
// selection.
func (p OpenAITokenPorts) Eligibility(ctx context.Context, key *apikey.APIKey, sub *billing.UserSubscription) error {
	keyCopy := apikey.CopyAPIKey(key)
	return p.Funding.CheckKey(ctx, keyCopy, sub, "", false)
}

// Platform returns an empty filter so compatible counting can select mixed
// platforms. The supplied key does not restrict this adapter's platform result;
// provider eligibility is determined by the group's route and selection rules.
func (OpenAITokenPorts) Platform(key *apikey.APIKey) string {
	return ""
}

// SessionHash delegates HTTP/body identity to the token execution service. The
// OpenAISessionInput argument is ignored here, so the token entry's supplied
// purpose uses the same TokenSessionHash operation for selection.
func (p OpenAITokenPorts) SessionHash(c *gin.Context, _ OpenAISessionInput, body []byte) string {
	return p.Execution.TokenSessionHash(c, body)
}

// AuthLatency publishes token-entry preparation duration in milliseconds to Ops
// observations.
func (OpenAITokenPorts) AuthLatency(c *gin.Context, ms int64) {
	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, ms)
}

// tokenSelectionError classifies counting selection using the effective key's
// group. The routing model drives availability diagnosis, while displayModel
// drives client text. Model-not-found results mark a local configuration
// limitation in Gin observations.
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

// OpenAICompatibleSelectionErrorForLog rewrites provider labels for Grok
// diagnostic messages. Nil errors and other platforms pass through. A rewritten
// message becomes a new plain error, so callers use the result for logging
// rather than error-chain inspection.
func OpenAICompatibleSelectionErrorForLog(err error, platform string) error {
	if err == nil || platform != "grok" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), "OpenAI providers", "Grok providers")
	if message == err.Error() {
		return err
	}
	// This replacement is diagnostic text for Grok callers. A fresh error
	// intentionally carries that message alone, so upstream chain
	// classification uses err instead.
	return fmt.Errorf("%s", message)
}
