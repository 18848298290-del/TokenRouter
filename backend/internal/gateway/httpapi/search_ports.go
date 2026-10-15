package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// search_ports.go connects standalone search admission, selected-provider
// execution and completion recording to HTTP request state.
// Standalone search executes through a retained credential-bearing target
// while the search loop carries provider IDs and slot evidence separately.
// Successful selection replaces that target; execution and completion then
// use it, so callbacks follow one serial request sequence and do not
// reconstruct credentials from their ID arguments.
//
// Waiting admission tracks queue capacity before requesting a provider slot
// and retires only a wait count that was admitted. Counter failures are
// logged while the slot wait continues. Successful completion snapshots
// query, Key, provider and HTTP observations before worker submission;
// mandatory submission can execute inline when no pool exists or work is
// rejected. That capture occurs while the HTTP handler still owns the
// provider lease.

// StandaloneSearchTarget binds upstream search execution and completion
// identity for one selected provider.
type StandaloneSearchTarget interface {
	// CompletionRecord exposes the selected provider record used for successful
	// search capture. The run reads it synchronously before CaptureMessages
	// snapshots recording data; callers keep the returned record stable during
	// that read.
	CompletionRecord() *providercore.Record
	// Execute sends a prepared native search body through this selected target
	// and returns response bytes or failure. The request loop retains provider
	// capacity during execution and interprets replayable failure before
	// releasing an attempt.
	Execute(context.Context, []byte) ([]byte, error)
}

// StandaloneSearchSelector couples a credential-bearing candidate with its
// scheduling and availability observations.
type StandaloneSearchSelector interface {
	// Select resolves a target for the supplied group and model while excluding
	// failed provider IDs. Its target and Selection describe the same candidate;
	// acquired or waiting slot evidence is consumed before execution.
	Select(context.Context, int64, string, map[int64]struct{}) (StandaloneSearchTarget, searchtools.Selection, bool, error)
}

// SearchPorts binds standalone admission, selected execution and
// application-managed completion services for HTTP search.
type SearchPorts struct {
	// Selector, Funding, Moderation, Concurrency, Keys, Recorder and Workers
	// remain application-managed services when Run copies this dependency value.
	// Selector, Funding and Recorder are required when execution reaches their
	// phases; optional dependencies retain the conditions described below.
	Selector StandaloneSearchSelector    // selected search target resolver
	Funding  *admission.FundingAdmission // initial funding and RPM admission
	// Moderation optionally checks normalized query content through
	// RunContentModeration. Its nil or disabled state permits the request when
	// the shared reader returns no blocking decision.
	Moderation ModerationPort
	// Concurrency supplies waited-for provider admission when selection did not
	// acquire a slot. Nil rejects that waiting path while already acquired
	// selection remains usable.
	Concurrency *ConcurrencyHelper
	// Keys enables Key quota updates in CaptureMessages when present. Nil leaves
	// that captured update flag disabled while other recording fields remain
	// available.
	Keys     gatewaycapture.QuotaUpdater
	Recorder *completion.Recorder // captured search completion recorder
	// Workers optionally accepts captured recording tasks. Mandatory rejected
	// work runs inline through completion submission; a nil pool also selects
	// synchronous execution in the request caller.
	Workers *completion.UsageRecordWorkerPool
}

// gatewayStandaloneSearchRun retains one HTTP search sequence and its latest
// successfully selected target.
type gatewayStandaloneSearchRun struct {
	// ports, c, groupID and isX are captured by Run for one request sequence.
	// The copied dependency value still shares services, while group and tool
	// mode stay fixed during selection and execution.
	ports SearchPorts // captured shared search dependencies
	// c remains the mutable request used for slot waiting. Keep it valid during
	// synchronous run methods; completion snapshots its observations before
	// scheduling recording work.
	c       *gin.Context // retained HTTP search request
	groupID int64        // selected Key group identifier
	isX     bool         // selected native X-search mode
	// target changes only after selection reports found without an error.
	// Execute and Complete read this target rather than callback provider-ID or
	// result arguments; invoke the run methods serially.
	target StandaloneSearchTarget
}

// DefaultModel reads the runtime Grok text model for each standalone search.
// Native resolution trims that setting and supplies its fixed default when
// blank; WebSearch calls this before access admission.
func (p SearchPorts) DefaultModel() string { return gatewaycapture.GrokStandaloneSearchModel() }

// NormalizeMaxResults resolves the client result limit with native Grok
// rules. Nonpositive input becomes five, positive values are capped at
// twenty, and WebSearch keeps the optional original request field for native
// builders.
func (p SearchPorts) NormalizeMaxResults(n int) int {
	return gatewaycapture.GrokStandaloneSearchMaxResults(n)
}

// Access reads the authenticated Key and supplies standalone group
// eligibility. Missing or nil Key returns false; a present group receives the
// Grok eligibility label for this search interface, while application
// selection restricts actual providers to Grok. GroupID retains the Key's
// optional pointer until the handler reads it.
func (p SearchPorts) Access(c *gin.Context) (SearchAccess, bool) {
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	if !ok || key == nil {
		return SearchAccess{}, false
	}
	out := SearchAccess{GroupPresent: key.Group != nil, GroupID: key.GroupID}
	if key.Group != nil {
		out.Platform = capability.PlatformGrok
	}
	return out, true
}

// Billing checks initial funding and optional RPM with the authenticated Key
// and resolved subscription. Call it after successful access admission with
// usable Funding; mapped failure details reach WebSearch before moderation or
// provider waiting.
func (p SearchPorts) Billing(c *gin.Context) *SearchHTTPFailure {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	sub, _ := SubscriptionFromContext(c)
	if err := p.Funding.CheckKey(c.Request.Context(), key, sub, "", false); err != nil {
		status, code, message, retry := BillingErrorDetails(err)
		return &SearchHTTPFailure{Status: status, Code: code, Message: message, RetryAfter: retry}
	}
	return nil
}

// Moderate checks normalized query content under the OpenAI Chat protocol and
// supplied model. It copies Key data for the shared moderation reader,
// includes the current authentication subject, and returns client details
// only for a blocking decision.
func (p SearchPorts) Moderate(c *gin.Context, model string, body []byte) *SearchHTTPFailure {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subject, _ := authctx.GetAuthSubjectFromContext(c)
	decision := RunContentModeration(GatewayModerationEndpoints{}, c, RequestLogger(c, "handler.gateway.web_search"), p.Moderation, apikey.CopyAPIKey(key), subject, moderation.ContentModerationProtocolOpenAIChat, model, body)
	if decision != nil && decision.Blocked {
		return &SearchHTTPFailure{Status: ContentModerationStatus(decision), Code: ContentModerationErrorCode(decision), Message: decision.Message}
	}
	return nil
}

// Run captures shared dependencies, Gin request, group ID and native search
// mode in fresh selection state. Drive this returned run serially through
// selection, admission, execution and successful completion while its request
// remains usable.
func (p SearchPorts) Run(c *gin.Context, groupID int64, isX bool) SearchHTTPRun {
	return &gatewayStandaloneSearchRun{ports: p, c: c, groupID: groupID, isX: isX}
}

// ConcurrencyError maps provider admission failure through the shared
// concurrency formatter and nonstreaming Anthropic error writer. It requires
// a usable request writer; this stage therefore uses the adapter's envelope
// rather than searchError JSON.
func (p SearchPorts) ConcurrencyError(c *gin.Context, err error) {
	status, kind, code, message := ConcurrencyErrorResponse(err, "provider")
	WriteAnthropicStreamError(c, status, kind, code, message, false, MarkOpsStreamError)
}

// Select retains a credential-bearing target only when selection finds a
// candidate without error. The returned scheduling evidence describes that
// candidate; a failed or absent selection leaves any prior target retained,
// so Execute follows only successful admission for the selected sequence.
func (r *gatewayStandaloneSearchRun) Select(ctx context.Context, model string, excluded map[int64]struct{}) (searchtools.Selection, bool, error) {
	target, selected, found, err := r.ports.Selector.Select(ctx, r.groupID, model, excluded)
	if err != nil || !found {
		return selected, found, err
	}
	r.target = target
	return selected, true, nil
}

// CanSwitch reports whether a typed upstream failure permits another-provider
// search recovery. Wrapped failover errors are recognized through the error
// chain; other errors remain terminal for the standalone loop.
func (r *gatewayStandaloneSearchRun) CanSwitch(err error) bool {
	var failure *forwardcore.UpstreamFailoverError
	return errors.As(err, &failure) && failure.ShouldRetryNextProvider()
}

// Execute builds Web or X search input and sends it through the retained
// target. It follows successful selection and acquisition, using target state
// instead of the provider-ID argument. Web construction receives the
// normalized limit; X construction reads native options retained in request.
// Response extraction applies the resolved limit and returns the grok-native
// vendor label.
func (r *gatewayStandaloneSearchRun) Execute(ctx context.Context, _ int64, request searchtools.StandaloneRequest, model string, maxResults int) (*contract.SearchResponse, string, error) {
	body, err := gatewaycapture.GrokStandaloneSearchBody(request, model, maxResults, r.isX)
	if err != nil {
		return nil, "", err
	}
	response, err := r.target.Execute(ctx, body)
	if err != nil {
		return nil, "", err
	}
	return gatewaycapture.GrokStandaloneSearchResponse(request.Query, response, maxResults), "grok-native", nil
}

// Acquire obtains selected search capacity and returns cleanup for attachment
// to the caller's request lease. Already acquired selection passes its
// release through. Otherwise a wait plan and usable concurrency service are
// needed; absence or rejected queue admission returns false.
//
// A wait-counter error is logged and slot waiting proceeds uncounted. An
// admitted counter is released after the slot wait returns, including an
// acquisition error, while acquired capacity is left to request cleanup.
// Queue admission uses ctx and slot waiting uses the retained Gin request
// context, so callers keep those request contexts aligned. Waiting remains
// nonstreaming and emits no SSE keepalive.
func (r *gatewayStandaloneSearchRun) Acquire(ctx context.Context, selected searchtools.Selection) (func(), bool, error) {
	if selected.Acquired {
		return selected.Release, true, nil
	}
	if selected.WaitPlan == nil || r.ports.Concurrency == nil {
		return nil, false, nil
	}
	counted := false
	wait, err := r.ports.Concurrency.EnterProviderWait(ctx, selected.ProviderID, selected.WaitPlan.MaxWaiting)
	// A failed queue counter leaves admission visibility unavailable. Continue
	// to the actual provider-slot wait, which still enforces concurrency, and
	// avoid releasing an uncounted queue entry.
	if err != nil {
		logging.L().Warn("gateway.web_search.provider_wait_counter_increment_failed", zap.Int64("provider_id", selected.ProviderID), zap.Error(err))
	} else if !wait.Allowed {
		return nil, false, nil
	} else {
		counted = true
	}
	streamStarted := false
	release, err := r.ports.Concurrency.AcquireProviderSlotWithWaitTimeout(r.c, selected.ProviderID, selected.WaitPlan.MaxConcurrency, selected.WaitPlan.Timeout, false, &streamStarted)
	// Retire admitted queue capacity after slot waiting finishes, including
	// failure, before returning any acquired release for attachment to the
	// request lease.
	if counted {
		wait.Release()
	}
	if err != nil {
		return nil, false, err
	}
	return release, true, nil
}

// Complete captures one successful standalone search for mandatory recording
// using the retained target. The caller provides the authenticated Key and
// matching Web or X mode while provider capacity is still retained. The
// execution-result argument is unused; the recording model denotes one tool
// operation with zero duration and the query supplies payload correlation.
//
// HTTP endpoint, agent and client-IP observations are read before
// CaptureMessages snapshots funding and provider data. The queued closure
// uses that captured input and application recorder, allowing worker
// execution after request cleanup. Rejected mandatory work or a missing
// worker pool uses the submission helper's inline path. Recording errors are
// logged with captured identities and do not become an HTTP search failure.
func (r *gatewayStandaloneSearchRun) Complete(c *gin.Context, req searchtools.StandaloneRequest, _ searchtools.StandaloneResult, isXSearch bool) {
	ports := r.ports
	provider := r.target.CompletionRecord()
	apiKey, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := SubscriptionFromContext(c)
	searchLabel := "web_search"
	if isXSearch {
		searchLabel = "x_search"
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := clientip.GetClientIP(c)
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, provider.Platform)
	requestPayloadHash := billing.HashUsageRequestPayload([]byte(req.Query))

	// Capture request and funding observations while Gin and the selected target
	// remain available. The worker closure reads completionInput, so later
	// request cleanup cannot replace its recording identities.
	completionInput := gatewaycapture.CaptureMessages(CompletionContext(c), &gatewaycapture.MessagesCapture{
		Result: &forwardcore.MessagesResult{
			Model:       "grok-" + strings.ReplaceAll(searchLabel, "_", "-"),
			SearchCount: 1,
			Duration:    0,
		},
		APIKey:             apiKey,
		User:               apiKey.User,
		Provider:           provider,
		Subscription:       subscription,
		InboundEndpoint:    inboundEndpoint,
		UpstreamEndpoint:   upstreamEndpoint,
		UserAgent:          userAgent,
		IPAddress:          clientIP,
		RequestPayloadHash: requestPayloadHash,
		APIKeyService:      ports.Keys,
	})
	completionRuntime := ports.Recorder
	// Standalone search records one operation even with no token measurement.
	// Mandatory submission retains recording work through queue rejection by
	// using the completion helper's synchronous fallback.
	NewCompletionSubmission(ports.Workers, false).SubmitMandatory(c, func(ctx context.Context) {
		// Recording failure is observed after search execution succeeds. Log
		// captured identities in this task, leaving the already selected HTTP
		// result independent of persistence failure.
		if err := completionRuntime.Record(ctx, completionInput, false); err != nil {
			logging.L().With(
				zap.String("component", "handler.gateway.web_search"),
				zap.Int64("user_id", completionInput.User.ID),
				zap.Int64("api_key_id", completionInput.APIKey.ID),
				zap.Int64("provider_id", completionInput.Provider.ID),
			).Error("gateway.web_search.record_usage_failed", zap.Error(err))
		}
	})
}
