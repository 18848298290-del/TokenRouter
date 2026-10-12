package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaylive "github.com/TokenFlux/TokenRouter/internal/gateway/live"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// LiveExecution provides session creation, lookup, and proxying for Live.
type LiveExecution interface {
	Create(context.Context, *session.LiveCallRequest, session.LiveCallIdentity, int) (*gatewaylive.Created, error)
	Lookup(context.Context, string, session.LiveCallIdentity) (*session.LiveCallRecord, error)
	Proxy(context.Context, *session.LiveCallRecord, *coderws.Conn) error
}

// LiveSlots acquires user slots immediately, without the ordinary request wait queue.
type LiveSlots interface {
	AcquireUserSlot(context.Context, int64, int) (*scheduler.AcquireResult, error)
}

// LivePorts binds moderation, funding, concurrency, and Live execution.
type LivePorts struct {
	Execution  LiveExecution
	Funding    TokenFunding
	Slots      LiveSlots
	Moderation ModerationPort
}

func (a LivePorts) APIKey(c *gin.Context) (*LiveAPIKey, bool) {
	key, ok := keyhttp.GetAPIKeyFromContext(c)
	if !ok {
		return nil, false
	}
	result := &LiveAPIKey{ID: key.ID, UserID: key.UserID, TeamID: key.TeamID, GroupID: key.GroupID, ModelMapping: apikey.CloneModelMapping(key.ModelMapping)}
	if key.Group != nil {
		result.Group = &LiveGroup{AllowLive: key.Group.AllowLive}
	}
	return result, true
}

func (a LivePorts) Subject(c *gin.Context) (LiveSubject, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	return LiveSubject{UserID: subject.UserID, Concurrency: subject.Concurrency}, ok
}

func (a LivePorts) Subscription(c *gin.Context) (*LiveSubscription, bool) {
	sub, ok := SubscriptionFromContext(c)
	if sub == nil {
		return nil, ok
	}
	return &LiveSubscription{ID: sub.ID}, ok
}

func (a LivePorts) Redirect(ctx context.Context, key *LiveAPIKey, model string) (context.Context, string) {
	return APIKeyModelRedirectContext(ctx, &apikey.APIKey{ID: key.ID, ModelMapping: key.ModelMapping}, model)
}

func (a LivePorts) Moderate(c *gin.Context, key *LiveAPIKey, subject LiveSubject, clientModel, model string, body []byte) bool {
	SetOpsRequestContext(c, clientModel, false)
	SetOpsEndpointContext(c, "", int16(usage.RequestTypeLive))
	log := RequestLogger(c, "handler.openai_gateway.live", zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", key.ID), zap.Any("group_id", key.GroupID))
	actualKey, _ := keyhttp.GetAPIKeyFromContext(c)
	actualSubject, _ := authctx.GetAuthSubjectFromContext(c)
	SetOpenAICyberWarningRequestSnapshot(c, moderation.ContentModerationProtocolOpenAIResponses, body)
	decision := RunContentModeration(GatewayModerationEndpoints{}, c, log, a.Moderation, apikey.CopyAPIKey(actualKey), actualSubject, moderation.ContentModerationProtocolOpenAIResponses, model, body)
	if decision != nil && decision.Blocked {
		a.Error(c, ContentModerationStatus(decision), ContentModerationErrorCode(decision), decision.Message)
		return false
	}
	return true
}

func (a LivePorts) CheckBilling(c *gin.Context) bool {
	if a.Funding == nil {
		a.Error(c, http.StatusServiceUnavailable, "api_error", "Billing service unavailable")
		return false
	}
	key, _ := keyhttp.GetAPIKeyFromContext(c)
	subscription, _ := SubscriptionFromContext(c)
	if err := a.Funding.CheckKey(c.Request.Context(), key, subscription, "", false); err != nil {
		status, code, message, retry := BillingErrorDetails(err)
		if retry > 0 {
			c.Header("Retry-After", strconv.Itoa(retry))
		}
		a.Error(c, status, code, message)
		return false
	}
	return true
}

func (a LivePorts) TryAcquireUserSlot(ctx context.Context, userID int64, limit int) (func(), bool, error) {
	result, err := a.Slots.AcquireUserSlot(ctx, userID, limit)
	if err != nil {
		return nil, false, err
	}
	if !result.Acquired {
		return nil, false, nil
	}
	return result.ReleaseFunc, true, nil
}

func (a LivePorts) InboundEndpoint(c *gin.Context) string {
	return GetInboundEndpoint(c)
}

func (a LivePorts) Create(ctx context.Context, request *session.LiveCallRequest, identity session.LiveCallIdentity, limit int) (*gatewaylive.Created, error) {
	return a.Execution.Create(ctx, request, identity, limit)
}

func (a LivePorts) Lookup(ctx context.Context, id string, identity session.LiveCallIdentity) (*session.LiveCallRecord, error) {
	return a.Execution.Lookup(ctx, id, identity)
}

func (a LivePorts) Proxy(ctx context.Context, record *session.LiveCallRecord, conn *coderws.Conn) error {
	return a.Execution.Proxy(ctx, record, conn)
}

func (a LivePorts) Error(c *gin.Context, status int, code, message string) {
	writeOpenAIRequestError(c, status, code, message, StopOpenAICompactSSEKeepaliveCommitted, MarkOpsStreamError, func(c *gin.Context) (string, string) { return ErrorRequestID(c), ErrorRequestModel(c) })
}

func (a LivePorts) PolicyDenied(c *gin.Context) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

func (a LivePorts) UpstreamStatus(err error) int {
	if upstream, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		return upstream.StatusCode
	}
	return 0
}
