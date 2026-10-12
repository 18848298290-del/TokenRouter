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

// StandaloneSearchTarget executes a selected provider request and supplies its completion record.
type StandaloneSearchTarget interface {
	CompletionRecord() *providercore.Record
	Execute(context.Context, []byte) ([]byte, error)
}

// StandaloneSearchSelector selects a target and its scheduling metadata for an HTTP search.
type StandaloneSearchSelector interface {
	Select(context.Context, int64, string, map[int64]struct{}) (StandaloneSearchTarget, searchtools.Selection, bool, error)
}

// SearchPorts holds application dependencies; each run retains its selected provider.
type SearchPorts struct {
	Selector    StandaloneSearchSelector
	Funding     *admission.FundingAdmission
	Moderation  ModerationPort
	Concurrency *ConcurrencyHelper
	Keys        gatewaycapture.QuotaUpdater
	Recorder    *completion.Recorder
	Workers     *completion.UsageRecordWorkerPool
}

// gatewayStandaloneSearchRun executes through the selected provider and captures completion data synchronously.
type gatewayStandaloneSearchRun struct {
	ports   SearchPorts
	c       *gin.Context
	groupID int64
	isX     bool
	target  StandaloneSearchTarget
}

func (p SearchPorts) DefaultModel() string { return gatewaycapture.GrokStandaloneSearchModel() }
func (p SearchPorts) NormalizeMaxResults(n int) int {
	return gatewaycapture.GrokStandaloneSearchMaxResults(n)
}

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

func (p SearchPorts) Billing(c *gin.Context) *SearchHTTPFailure {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	sub, _ := SubscriptionFromContext(c)
	if err := p.Funding.CheckKey(c.Request.Context(), key, sub, "", false); err != nil {
		status, code, message, retry := BillingErrorDetails(err)
		return &SearchHTTPFailure{Status: status, Code: code, Message: message, RetryAfter: retry}
	}
	return nil
}

func (p SearchPorts) Moderate(c *gin.Context, model string, body []byte) *SearchHTTPFailure {
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subject, _ := authctx.GetAuthSubjectFromContext(c)
	decision := RunContentModeration(GatewayModerationEndpoints{}, c, RequestLogger(c, "handler.gateway.web_search"), p.Moderation, apikey.CopyAPIKey(key), subject, moderation.ContentModerationProtocolOpenAIChat, model, body)
	if decision != nil && decision.Blocked {
		return &SearchHTTPFailure{Status: ContentModerationStatus(decision), Code: ContentModerationErrorCode(decision), Message: decision.Message}
	}
	return nil
}

func (p SearchPorts) Run(c *gin.Context, groupID int64, isX bool) SearchHTTPRun {
	return &gatewayStandaloneSearchRun{ports: p, c: c, groupID: groupID, isX: isX}
}

func (p SearchPorts) ConcurrencyError(c *gin.Context, err error) {
	status, kind, code, message := ConcurrencyErrorResponse(err, "provider")
	WriteAnthropicStreamError(c, status, kind, code, message, false, MarkOpsStreamError)
}

func (r *gatewayStandaloneSearchRun) Select(ctx context.Context, model string, excluded map[int64]struct{}) (searchtools.Selection, bool, error) {
	target, selected, found, err := r.ports.Selector.Select(ctx, r.groupID, model, excluded)
	if err != nil || !found {
		return selected, found, err
	}
	r.target = target
	return selected, true, nil
}

func (r *gatewayStandaloneSearchRun) CanSwitch(err error) bool {
	var failure *forwardcore.UpstreamFailoverError
	return errors.As(err, &failure) && failure.ShouldRetryNextProvider()
}

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

func (r *gatewayStandaloneSearchRun) Acquire(ctx context.Context, selected searchtools.Selection) (func(), bool, error) {
	if selected.Acquired {
		return selected.Release, true, nil
	}
	if selected.WaitPlan == nil || r.ports.Concurrency == nil {
		return nil, false, nil
	}
	counted := false
	wait, err := r.ports.Concurrency.EnterProviderWait(ctx, selected.ProviderID, selected.WaitPlan.MaxWaiting)
	if err != nil {
		logging.L().Warn("gateway.web_search.provider_wait_counter_increment_failed", zap.Int64("provider_id", selected.ProviderID), zap.Error(err))
	} else if !wait.Allowed {
		return nil, false, nil
	} else {
		counted = true
	}
	streamStarted := false
	release, err := r.ports.Concurrency.AcquireProviderSlotWithWaitTimeout(r.c, selected.ProviderID, selected.WaitPlan.MaxConcurrency, selected.WaitPlan.Timeout, false, &streamStarted)
	if counted {
		wait.Release()
	}
	if err != nil {
		return nil, false, err
	}
	return release, true, nil
}

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

	// Capture funding and request data before queueing so the worker uses this snapshot.
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
	NewCompletionSubmission(ports.Workers, false).SubmitMandatory(c, func(ctx context.Context) {
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
