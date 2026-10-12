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

// Live HTTP admission uses the authenticated Key owner beside the funding
// subject carried by the request. Model-mapping entries and the group feature
// flag are copied into a Live view before execution, while optional identity
// pointers remain borrowed until Creator reads them into its saved record.

// LiveExecution supplies saved-call creation, identity lookup and control
// proxying for HTTP.
type LiveExecution interface {
	// Create opens and saves a session while the HTTP caller holds ordinary user
	// admission. Successful execution owns a separate Live lease that remains
	// after the creation response returns.
	Create(context.Context, *session.LiveCallRequest, session.LiveCallIdentity, int) (*gatewaylive.Created, error)
	// Lookup validates saved call, Key, funding subject and group identity
	// before proxying. A mismatched identity, missing record or closed call
	// retains its execution error for HTTP presentation.
	Lookup(context.Context, string, session.LiveCallIdentity) (*session.LiveCallRecord, error)
	// Proxy relays an authenticated saved call through the supplied client
	// connection. Its caller retains client teardown, while execution claims the
	// control connection and manages upstream transport and session lifetime.
	Proxy(context.Context, *session.LiveCallRecord, *coderws.Conn) error
}

// LiveSlots supplies immediate ordinary user admission during Live session
// creation.
type LiveSlots interface {
	// AcquireUserSlot attempts ordinary user capacity immediately and returns
	// acquisition evidence. Successful acquisition supplies cleanup retained by
	// HTTP creation; this slot is separate from the Live session lease reserved
	// by execution.
	AcquireUserSlot(context.Context, int64, int) (*scheduler.AcquireResult, error)
}

// LivePorts binds shared funding, moderation, user admission and session
// execution services.
type LivePorts struct {
	Execution LiveExecution // shared Live session execution
	// Funding is required for creation eligibility. Missing binding produces a
	// temporary-service response before any user capacity is acquired.
	Funding TokenFunding
	Slots   LiveSlots // immediate ordinary user admission
	// Moderation is optional; RunContentModeration returns no decision when absent.
	// A supplied blocked decision stops creation before funding and session admission.
	Moderation ModerationPort
}

// APIKey builds a Live view from a usable authenticated middleware Key.
// Missing authentication returns nil and false; model mapping and group
// policy are copied, while optional team and group ID pointers retain their
// source storage.
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

// Subject returns the authenticated funding subject and concurrency with its
// presence flag. False tells the HTTP entry to present missing user context
// before funding and session resource acquisition.
func (a LivePorts) Subject(c *gin.Context) (LiveSubject, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	return LiveSubject{UserID: subject.UserID, Concurrency: subject.Concurrency}, ok
}

// Subscription returns optional funding identity as a new Live subscription
// value. A nil source retains the context presence flag with nil output;
// creation stores only a present subscription's scalar ID.
func (a LivePorts) Subscription(c *gin.Context) (*LiveSubscription, bool) {
	sub, ok := SubscriptionFromContext(c)
	if sub == nil {
		return nil, ok
	}
	return &LiveSubscription{ID: sub.ID}, ok
}

// Redirect applies Key mapping to the supplied model and retains its trace
// context. Callers provide a usable Live Key view and install the returned
// context before moderation or creating the session.
func (a LivePorts) Redirect(ctx context.Context, key *LiveAPIKey, model string) (context.Context, string) {
	return APIKeyModelRedirectContext(ctx, &apikey.APIKey{ID: key.ID, ModelMapping: key.ModelMapping}, model)
}

// Moderate records Live Ops metadata and checks rewritten session content
// under the authenticated request identity. A blocked decision is presented
// and returns false; absent or permitted moderation returns true for funding
// admission.
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

// CheckBilling checks current Key and subscription with funding and RPM
// policy. A missing funding service or check failure presents its classified
// response and returns false; positive retry delays are added before
// presentation.
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

// TryAcquireUserSlot obtains an immediate ordinary user slot through the
// bound service. An error or rejected acquisition returns false; successful
// admission transfers usable cleanup to the caller, which releases it after
// HTTP creation.
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

// InboundEndpoint returns normalized request entry metadata for the saved
// Live identity.
func (a LivePorts) InboundEndpoint(c *gin.Context) string {
	return GetInboundEndpoint(c)
}

// Create delegates opening and saving a Live session with captured identity
// and concurrency. Callers hold ordinary user admission while the execution
// implementation reserves the longer-lived Live session lease.
func (a LivePorts) Create(ctx context.Context, request *session.LiveCallRequest, identity session.LiveCallIdentity, limit int) (*gatewaylive.Created, error) {
	return a.Execution.Create(ctx, request, identity, limit)
}

// Lookup delegates saved call identity validation before client control
// upgrade. Missing or closed state and identity mismatch retain their
// execution errors, allowing HTTP entry to choose their distinct responses.
func (a LivePorts) Lookup(ctx context.Context, id string, identity session.LiveCallIdentity) (*session.LiveCallRecord, error) {
	return a.Execution.Lookup(ctx, id, identity)
}

// Proxy delegates the accepted client WebSocket to Live control execution.
// The HTTP owner closes the client connection after this call, while
// execution handles upstream controller ownership and any observer recovery.
func (a LivePorts) Proxy(ctx context.Context, record *session.LiveCallRecord, conn *coderws.Conn) error {
	return a.Execution.Proxy(ctx, record, conn)
}

// Error presents the supplied classification through shared OpenAI request
// error handling. The writer uses keepalive commitment, Ops marking and
// current request identity when choosing the available HTTP or stream error
// representation.
func (a LivePorts) Error(c *gin.Context, status int, code, message string) {
	writeOpenAIRequestError(c, status, code, message, StopOpenAICompactSSEKeepaliveCommitted, MarkOpsStreamError, func(c *gin.Context) (string, string) { return ErrorRequestID(c), ErrorRequestModel(c) })
}

// PolicyDenied records provider client policy rejection as an Ops business
// limitation.
func (a LivePorts) PolicyDenied(c *gin.Context) {
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

// UpstreamStatus returns the status carried by a classified upstream failure
// in the error chain. Another error or nil returns zero, leaving
// WriteLiveCreateError to select generic upstream presentation.
func (a LivePorts) UpstreamStatus(err error) int {
	if upstream, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		return upstream.StatusCode
	}
	return 0
}
