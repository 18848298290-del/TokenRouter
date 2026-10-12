package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// CountTarget represents a selected token-count provider and exposes its execution and session cleanup.
type CountTarget interface {
	Snapshot() provider.ProviderSnapshot
	RetryLimit() int
	ForwardCountTokens(context.Context, *gin.Context, *requeststate.ParsedRequest) error
	ReleaseSession(context.Context, string)
}

// CountExecutor selects count providers without slots, plans group routes, and temporarily excludes retryable failures.
type CountExecutor interface {
	SelectCountTarget(context.Context, *int64, string, string, map[int64]struct{}) (CountTarget, error)
	PlanCountRoute(context.Context, *apikey.APIKey, string) routing.RoutePlan
	TempUnscheduleRetryableError(context.Context, int64, *forwardcore.UpstreamFailoverError)
}

// CountHTTPPorts binds shared dependencies and creates request-specific attempt state.
type CountHTTPPorts struct {
	ClientGroupFallback ClientGroupFallbackResolver
	Diagnoser           routing.ModelAvailabilityDiagnoser
	Executor            CountExecutor
	Funding             interface {
		CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	}
	ReadAccess           func(*gin.Context) (*apikey.APIKey, bool)
	ObserveCompatibility func(*zap.Logger)
	BusinessError        func(*gin.Context, error, bool, func(int, string, string, bool)) bool
	Failure              func(*gin.Context, *forwardcore.UpstreamFailoverError, string, bool)
}

type countAttempt struct {
	ports           CountHTTPPorts
	c               *gin.Context
	key             *apikey.APIKey
	parsed, attempt *requeststate.ParsedRequest
	hash            string
	log             *zap.Logger
	target          CountTarget
}

// FailoverClientGone ends failover when the request context is canceled.
// Reselecting with a canceled context could misreport provider exhaustion as 502; detached in-flight work still
// settles.
// An uncommitted response gets status 499 for access logging.
func FailoverClientGone(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Context().Err() == nil {
		return false
	}
	// Stop heartbeats and wait for their writes before setting the status.
	// A heartbeat that already committed HTTP 200 keeps that status.
	if StopOpenAICompactSSEKeepaliveCommitted(c) {
		return true
	}
	if !c.Writer.Written() {
		c.Status(StatusClientClosedRequest)
	}
	return true
}

func (p CountHTTPPorts) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	key, ok := p.ReadAccess(c)
	return apikey.CopyAPIKey(key), ok
}

func (p CountHTTPPorts) CompatibilityMetrics(log *zap.Logger) { p.ObserveCompatibility(log) }

func (p CountHTTPPorts) ObserveRequest(c *gin.Context, model string, stream bool) {
	SetOpsRequestContext(c, model, stream)
}

func (p CountHTTPPorts) ObserveEndpoint(c *gin.Context, stream bool) {
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
}

func (p CountHTTPPorts) BindClient(c *gin.Context, d ClientDetection) {
	ctx := requeststate.SetClaudeCodeClient(c.Request.Context(), d.ClaudeCode)
	if d.ClaudeCode && d.Version != "" {
		ctx = requeststate.SetClaudeCodeVersion(ctx, d.Version)
	}
	c.Request = c.Request.WithContext(ctx)
}

func (p CountHTTPPorts) BindThinking(c *gin.Context, thinking bool) {
	c.Request = c.Request.WithContext(requeststate.WithThinkingEnabled(c.Request.Context(), thinking))
}

func (p CountHTTPPorts) Eligibility(ctx context.Context, key *apikey.APIKey, sub *billing.UserSubscription) error {
	value := apikey.CopyAPIKey(key)
	return p.Funding.CheckKey(ctx, value, sub, "", false)
}

func (p CountHTTPPorts) FailoverObservation(ctx context.Context, event string, values map[string]any) {
	telemetry.Failover(ctx, event, values)
}

func (p CountHTTPPorts) Execution(c *gin.Context, key *apikey.APIKey, parsed *requeststate.ParsedRequest, hash string, log *zap.Logger) textflow.CountPorts {
	plan := p.Executor.PlanCountRoute(c.Request.Context(), key, parsed.Model).WithClientProtocol(protocol.ProtocolAnthropicMessages)
	ctx := requeststate.WithRoutePlan(requeststate.WithGroup(c.Request.Context(), key.Group), plan)
	c.Request = c.Request.WithContext(ctx)
	return &countAttempt{ports: p, c: c, key: apikey.CopyAPIKey(key), parsed: parsed, hash: hash, log: log}
}

// PrepareGroupAttempt clones the original body and resolves the current group mapping independently for each attempt.
func PrepareGroupAttempt(ctx context.Context, parsed *requeststate.ParsedRequest, body []byte, key *apikey.APIKey, requested string, plan func(context.Context, *apikey.APIKey, string) routing.RoutePlan) (*requeststate.ParsedRequest, routing.GroupMappingResult, error) {
	attempt, err := parsed.CloneForBody(body)
	if err != nil {
		return nil, routing.GroupMappingResult{}, err
	}
	var groupID *int64
	if key != nil && key.GroupID != nil {
		id := *key.GroupID
		groupID = &id
	}
	attempt.GroupID = groupID
	mapping := plan(ctx, key, requested).Mapping()
	if !mapping.Mapped {
		return attempt, mapping, nil
	}
	attempt.Model = mapping.MappedModel
	if err := attempt.ReplaceBody(openaiprotocol.ReplaceModelInBody(attempt.Body.Bytes(), mapping.MappedModel)); err != nil {
		return nil, routing.GroupMappingResult{}, err
	}
	return attempt, mapping, nil
}

func (b *countAttempt) Context() context.Context { return b.c.Request.Context() }

func (b *countAttempt) Select(excluded map[int64]struct{}) (textflow.Selection, error) {
	target, err := b.ports.Executor.SelectCountTarget(b.Context(), b.key.GroupID, b.hash, b.parsed.Model, excluded)
	if err != nil {
		return textflow.Selection{}, err
	}
	b.target = target
	value := target.Snapshot()
	SetOpsSelectedProvider(b.c, value.ID, value.Platform)
	return textflow.Selection{Provider: value, RetryLimit: target.RetryLimit()}, nil
}

func (b *countAttempt) SelectionFailed(err error, last *textflow.AttemptFailure) {
	b.log.Warn("gateway.count_tokens_select_provider_failed", zap.Error(err))
	if last != nil {
		b.exhausted(last, "")
		return
	}
	if b.ports.BusinessError(b.c, err, false, func(status int, kind, message string, _ bool) { WriteAnthropicError(b.c, status, kind, "", message) }) {
		return
	}
	result := ClassifySelectionError(b.Context(), b.ports.Diagnoser, b.key.GroupID, b.parsed.Model, b.parsed.Model, "")
	if result.ModelNotFound {
		MarkOpsClientBusinessLimited(b.c, OpsClientBusinessLimitedReasonLocalModelConfiguration)
	} else {
		MarkOpsRoutingCapacityLimitedIfNoAvailable(b.c, err)
	}
	WriteAnthropicError(b.c, result.Status, result.ErrType, "", result.Message)
}

func (b *countAttempt) Prepare(_ textflow.Selection) bool {
	var err error
	b.attempt, _, err = PrepareGroupAttempt(b.Context(), b.parsed, b.parsed.Body.Bytes(), b.key, b.parsed.Model, b.ports.Executor.PlanCountRoute)
	if err != nil {
		WriteAnthropicError(b.c, http.StatusBadRequest, "invalid_request_error", "", "Failed to parse request body")
		return false
	}
	return true
}

func (b *countAttempt) Forward(_ textflow.Selection) *textflow.AttemptFailure {
	err := b.target.ForwardCountTokens(b.Context(), b.c, b.attempt)
	if err == nil {
		return nil
	}
	if original, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		return &textflow.AttemptFailure{Cause: err, Policy: original.RetryFailure()}
	}
	return &textflow.AttemptFailure{Cause: err}
}

func (b *countAttempt) ForwardFailed(selected textflow.Selection, err error) {
	b.log.Error("gateway.count_tokens_forward_failed", zap.Int64("provider_id", selected.Provider.ID), zap.Error(err))
}

func (b *countAttempt) ReleaseSession(_ textflow.Selection) {
	b.target.ReleaseSession(context.Background(), b.hash)
}

func (b *countAttempt) Canceled() { FailoverClientGone(b.c) }

func (b *countAttempt) Exhausted(selected textflow.Selection, last *textflow.AttemptFailure) {
	b.exhausted(last, selected.Provider.Platform)
}

func (b *countAttempt) exhausted(last *textflow.AttemptFailure, platform string) {
	var original *forwardcore.UpstreamFailoverError
	if last != nil {
		errors.As(last.Cause, &original)
	}
	b.ports.Failure(b.c, original, platform, false)
}

func (b *countAttempt) TempUnscheduleRetryableError(ctx context.Context, id int64, failure *textflow.AttemptFailure) {
	if original, ok := errors.AsType[*forwardcore.UpstreamFailoverError](failure.Cause); ok {
		b.ports.Executor.TempUnscheduleRetryableError(ctx, id, original)
	}
}

func (p CountHTTPPorts) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if p.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return p.ClientGroupFallback(ctx, key, source)
}
