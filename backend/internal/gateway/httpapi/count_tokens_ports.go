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

// count_tokens_ports.go adapts provider selection, mapped request preparation,
// and failure output to the Messages count loop.
// Auxiliary count attempts select a provider and prepare mapped input while
// leaving generation slot admission to other request flows. The text count loop
// stores failure history and decides retry actions; these HTTP operations bind
// one current target and translate exhausted or canceled work into client output.
//
// Each preparation begins from the entry's parsed body so an earlier provider's
// mapped model cannot become the next attempt's input model. Parsed structures
// are copied with fresh body references, while unchanged bytes and session
// metadata can remain shared. Failed forwarding releases the selected session
// through a background context before the count loop reselects or finishes.

// CountTarget represents a selected token-count provider and exposes its execution and session cleanup.
type CountTarget interface {
	// Snapshot returns provider facts used by selection observations and retry decisions.
	Snapshot() provider.ProviderSnapshot
	// RetryLimit returns the selected provider's same-provider count retry allowance.
	RetryLimit() int
	// ForwardCountTokens handles one prepared request using this selected target.
	// Nil error ends the count loop, including a locally written unsupported
	// response. Typed failover errors permit policy handling; other errors terminate after observation and cleanup.
	ForwardCountTokens(context.Context, *gin.Context, *requeststate.ParsedRequest) error
	// ReleaseSession relinquishes the selected provider's reservation for the supplied session hash.
	// The count loop calls this after failed forwarding before reselection or
	// terminal handling, and supplies a context independent of request cancellation.
	ReleaseSession(context.Context, string)
}

// CountExecutor selects count providers without slots, plans group routes, and temporarily excludes retryable failures.
type CountExecutor interface {
	// SelectCountTarget selects a usable count target under the group and exclusions.
	// Callers supply the original request model and session hash. Success must
	// return a target whose snapshot and count operations are available for this attempt.
	SelectCountTarget(context.Context, *int64, string, string, map[int64]struct{}) (CountTarget, error)
	// PlanCountRoute resolves the group mapping used when preparing count input.
	PlanCountRoute(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// TempUnscheduleRetryableError applies provider feedback from a typed count failure.
	// The count loop supplies the target ID and captured failure after policy
	// classifies it; the implementation owns the provider's scheduling side effect.
	TempUnscheduleRetryableError(context.Context, int64, *forwardcore.UpstreamFailoverError)
}

// CountHTTPPorts binds shared dependencies and creates request-specific attempt state.
type CountHTTPPorts struct {
	// ClientGroupFallback authorizes a target group when client restrictions apply.
	// A nil callback leaves those incompatible requests with the Claude Code-only error.
	ClientGroupFallback ClientGroupFallbackResolver
	Diagnoser           routing.ModelAvailabilityDiagnoser // unavailable-model diagnostic dependency
	Executor            CountExecutor                      // count target and route operations
	Funding             interface {
		// CheckKey validates the copied key and selected funding before count attempts.
		// Eligibility passes empty platform and afterWait=false; any returned
		// error ends HTTP preflight before route publication and provider selection.
		CheckKey(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error
	} // selected funding admission
	// ReadAccess supplies a key when EffectiveAPIKey is absent. Its returned key
	// is copied before preparation, while an effective key is returned as stored.
	ReadAccess           func(*gin.Context) (*apikey.APIKey, bool)
	ObserveCompatibility func(*zap.Logger) // compatibility metric observation
	// BusinessError can write a selection rejection through the supplied callback.
	// True means that output was handled; false permits model-availability classification.
	BusinessError func(*gin.Context, error, bool, func(int, string, string, bool)) bool
	// Failure writes the exhausted typed failure. A missing typed cause can pass
	// nil, and callers invoke this before returning from the terminal count path.
	Failure func(*gin.Context, *forwardcore.UpstreamFailoverError, string, bool)
}

// countAttempt retains one HTTP count request across selection and prepared attempts.
type countAttempt struct {
	ports CountHTTPPorts // bound count dependencies
	c     *gin.Context   // current HTTP request context
	key   *apikey.APIKey // copied request key view
	// parsed is the entry's base request, and attempt is replaced by Prepare
	// for each selected target. Unchanged bytes and session metadata can remain shared through shallow parsed copies.
	parsed, attempt *requeststate.ParsedRequest // parsed: original prepared input; attempt: current mapped input
	hash            string                      // selection session hash
	log             *zap.Logger                 // count request diagnostics
	// target is replaced after successful Select and retained on selection error.
	// Forward and ReleaseSession operate on this target under the count loop's serial call order.
	target CountTarget
}

// FailoverClientGone reports whether request cancellation has ended the retry window.
// Any request-context error, including a deadline, returns true. Missing request
// data or an active context returns false; callers stop their retry loop on true.
//
// Heartbeat stopping precedes status selection so the caller waits for current
// heartbeat writes before taking over output. A started heartbeat or already
// written response retains its status; other canceled requests select 499 for
// access observation. The helper's status decision leaves resource cleanup and
// any already captured completion work under their existing owners.
func FailoverClientGone(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Context().Err() == nil {
		return false
	}
	// A heartbeat may be writing as cancellation is observed. Wait for that
	// controller operation before choosing status, retaining framing it already selected for this response.
	if StopOpenAICompactSSEKeepaliveCommitted(c) {
		return true
	}
	if !c.Writer.Written() {
		c.Status(StatusClientClosedRequest)
	}
	return true
}

// Access prefers the effective key and otherwise copies the configured reader's view.
// The returned flag is the source's lookup flag, including true with a nil
// pointer when a source stores one; request preparation requires a usable key on success.
func (p CountHTTPPorts) Access(c *gin.Context) (*apikey.APIKey, bool) {
	if key, ok := EffectiveAPIKey(c); ok {
		return key, true
	}
	key, ok := p.ReadAccess(c)
	return apikey.CopyAPIKey(key), ok
}

// CompatibilityMetrics invokes the bound compatibility observer with the count request logger.
func (p CountHTTPPorts) CompatibilityMetrics(log *zap.Logger) { p.ObserveCompatibility(log) }

// ObserveRequest records parsed model and stream values in the HTTP Ops context.
func (p CountHTTPPorts) ObserveRequest(c *gin.Context, model string, stream bool) {
	SetOpsRequestContext(c, model, stream)
}

// ObserveEndpoint records the count request type inferred from its parsed stream flag.
func (p CountHTTPPorts) ObserveEndpoint(c *gin.Context, stream bool) {
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeFromLegacy(stream, false)))
}

// BindClient stores client recognition and conditionally publishes its detected version.
// A recognized client with a nonempty version replaces the version value.
// Otherwise any version already in context remains, while the recognition flag is updated.
func (p CountHTTPPorts) BindClient(c *gin.Context, d ClientDetection) {
	ctx := requeststate.SetClaudeCodeClient(c.Request.Context(), d.ClaudeCode)
	if d.ClaudeCode && d.Version != "" {
		ctx = requeststate.SetClaudeCodeVersion(ctx, d.Version)
	}
	c.Request = c.Request.WithContext(ctx)
}

// BindThinking stores parsed thinking intent for count execution's model resolution.
func (p CountHTTPPorts) BindThinking(c *gin.Context, thinking bool) {
	c.Request = c.Request.WithContext(requeststate.WithThinkingEnabled(c.Request.Context(), thinking))
}

// Eligibility validates funding using a copied key and the supplied subscription pointer.
// It requests the funding dependency's initial admission path with empty
// platform and afterWait=false, returning errors directly to HTTP preflight.
func (p CountHTTPPorts) Eligibility(ctx context.Context, key *apikey.APIKey, sub *billing.UserSubscription) error {
	value := apikey.CopyAPIKey(key)
	return p.Funding.CheckKey(ctx, value, sub, "", false)
}

// FailoverObservation forwards count-loop events and values to gateway telemetry recording.
func (p CountHTTPPorts) FailoverObservation(ctx context.Context, event string, values map[string]any) {
	telemetry.Failover(ctx, event, values)
}

// Execution publishes the count route and creates serial request-specific attempt operations.
// Callers supply a usable key and parsed request after funding preflight.
// The initial plan receives Messages protocol, and attempt state retains a copied
// key alongside the original parsed input and selection hash.
func (p CountHTTPPorts) Execution(c *gin.Context, key *apikey.APIKey, parsed *requeststate.ParsedRequest, hash string, log *zap.Logger) textflow.CountPorts {
	plan := p.Executor.PlanCountRoute(c.Request.Context(), key, parsed.Model).WithClientProtocol(protocol.ProtocolAnthropicMessages)
	ctx := requeststate.WithRoutePlan(requeststate.WithGroup(c.Request.Context(), key.Group), plan)
	c.Request = c.Request.WithContext(ctx)
	return &countAttempt{ports: p, c: c, key: apikey.CopyAPIKey(key), parsed: parsed, hash: hash, log: log}
}

// PrepareGroupAttempt copies parsed state and resolves group mapping for one attempt.
// CloneForBody creates a new body reference around the supplied bytes, which
// remain shared until replacement; SessionContext is also retained by reference.
// The upstream-accepted callback is cleared by cloning, and the group ID receives
// a separate pointer copied from the key when present.
//
// Mapping starts from requested on each call, preventing a previous attempt's
// mapped model from becoming this attempt's mapping source. Unmapped preparation
// keeps supplied bytes; mapped preparation replaces the body and refreshes its
// parsed fields. Callers leave shared input bytes and SessionContext unchanged
// while an attempt uses them, and provide a route planner. Clone or reparse
// failures terminate preparation with an error.
func PrepareGroupAttempt(ctx context.Context, parsed *requeststate.ParsedRequest, body []byte, key *apikey.APIKey, requested string, plan func(context.Context, *apikey.APIKey, string) routing.RoutePlan) (*requeststate.ParsedRequest, routing.GroupMappingResult, error) {
	attempt, err := parsed.CloneForBody(body)
	if err != nil {
		return nil, routing.GroupMappingResult{}, err
	}
	// Copy group identity onto this attempt rather than retaining the key's pointer.
	// Replanning can then replace attempt metadata while the base parsed request remains available for later tries.
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

// Context returns the current HTTP request context used by the count loop.
func (b *countAttempt) Context() context.Context { return b.c.Request.Context() }

// Select chooses and stores a count target using the original model and exclusions.
// Errors leave the previous target stored. Success records provider observations
// and returns its snapshot and retry limit; later operations use this selected target.
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

// SelectionFailed writes the terminal error after provider selection stops.
// A retained attempt failure takes precedence over current availability diagnosis.
// Otherwise business errors can claim output before model diagnosis selects a
// local configuration or routing-capacity observation and the Messages error.
//
// The retained cause comes from a prior forward attempt, while the current
// selection error can describe exhaustion of the remaining candidates. Giving
// the captured cause priority keeps its upstream evidence available to Failure.
// With no such cause, diagnostics use the original model and current group;
// business handling can finish first without adding a second error envelope.
func (b *countAttempt) SelectionFailed(err error, last *textflow.AttemptFailure) {
	b.log.Warn("gateway.count_tokens_select_provider_failed", zap.Error(err))
	if last != nil {
		// Selection can fail after a provider already returned a concrete failure.
		// Keep that captured response as the terminal evidence rather than replacing it with availability diagnosis.
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

// Prepare rebuilds mapped input from the original parsed body for the current attempt.
// A clone or reparse failure writes a local bad-request response and returns
// false, stopping the loop before forwarding this selected target.
func (b *countAttempt) Prepare(_ textflow.Selection) bool {
	var err error
	b.attempt, _, err = PrepareGroupAttempt(b.Context(), b.parsed, b.parsed.Body.Bytes(), b.key, b.parsed.Model, b.ports.Executor.PlanCountRoute)
	if err != nil {
		WriteAnthropicError(b.c, http.StatusBadRequest, "invalid_request_error", "", "Failed to parse request body")
		return false
	}
	return true
}

// Forward runs the stored target with the freshly prepared count request.
// Nil error ends the loop. A typed failover error adds retry policy to the
// captured cause, while other errors remain terminal failures for observation and session release.
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

// ForwardFailed logs a terminal count error with the selected provider identifier.
func (b *countAttempt) ForwardFailed(selected textflow.Selection, err error) {
	b.log.Error("gateway.count_tokens_forward_failed", zap.Int64("provider_id", selected.Provider.ID), zap.Error(err))
}

// ReleaseSession releases the stored count target's reservation using the original hash.
// A background context makes this cleanup independent of request cancellation.
// The loop calls it after failed forwarding and before reselection or terminal handling.
func (b *countAttempt) ReleaseSession(_ textflow.Selection) {
	// Failed forwarding can coincide with client cancellation. Release the target's
	// session through an independent context so canceled request work does not suppress that cleanup.
	b.target.ReleaseSession(context.Background(), b.hash)
}

// Canceled applies the HTTP cancellation status policy after the count loop stops.
func (b *countAttempt) Canceled() { FailoverClientGone(b.c) }

// Exhausted writes the retained failure using the last selected provider's platform.
func (b *countAttempt) Exhausted(selected textflow.Selection, last *textflow.AttemptFailure) {
	b.exhausted(last, selected.Provider.Platform)
}

// exhausted extracts a typed failure and invokes the bound terminal error writer.
// An absent last failure or unmatched cause passes nil to that writer.
// The supplied platform identifies the last selection, or remains empty after selection exhaustion.
func (b *countAttempt) exhausted(last *textflow.AttemptFailure, platform string) {
	var original *forwardcore.UpstreamFailoverError
	if last != nil {
		errors.As(last.Cause, &original)
	}
	b.ports.Failure(b.c, original, platform, false)
}

// TempUnscheduleRetryableError applies scheduling feedback when the cause contains typed upstream failure.
// The count loop supplies a nonnil failure. Causes without that type are
// ignored, leaving provider feedback to the classification that produced the attempt failure.
func (b *countAttempt) TempUnscheduleRetryableError(ctx context.Context, id int64, failure *textflow.AttemptFailure) {
	if original, ok := errors.AsType[*forwardcore.UpstreamFailoverError](failure.Cause); ok {
		b.ports.Executor.TempUnscheduleRetryableError(ctx, id, original)
	}
}

// ResolveClientGroup delegates target authorization to the bound client-group resolver.
// A nil callback returns the Claude Code-only error. Successful resolution
// supplies the key and subscription that HTTP preparation validates before publishing its selected group.
func (p CountHTTPPorts) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source protocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if p.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return p.ClientGroupFallback(ctx, key, source)
}
