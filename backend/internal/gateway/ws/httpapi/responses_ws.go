package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// ResponsesWSOptions configures ingress connections and first-frame budgets.
type ResponsesWSOptions struct {
	Runtime                        *gatewayws.Runtime
	MaxIngressConnectionsPerAPIKey int
	ReadLimit                      int64
	FirstMessageTimeout            time.Duration
	MaxProviderSwitches            int
}

// ResponsesWSCall contains upgraded request data passed to the dependency factory.
type ResponsesWSCall struct {
	Key                 *gatewayws.EntryKey
	Subject             gatewayws.EntrySubject
	Conn                *coderws.Conn
	Logger              *zap.Logger
	ClientIP, UserAgent string
}

// ResponsesWSBackend supplies authentication data and entry operations.
type ResponsesWSBackend interface {
	Access(*gin.Context) (*gatewayws.EntryKey, bool)
	Transport(*gin.Context)
	Error(*gin.Context, int, string, string)
	Dependencies(*gin.Context, *zap.Logger) bool
	SummarizeRead(error) (string, string)
	Entry(*gin.Context, ResponsesWSCall) gatewayws.EntryPorts
}
type ResponsesWSHandler struct {
	gatewayhttp.RequestLifetime

	options     ResponsesWSOptions
	backend     ResponsesWSBackend
	concurrency *gatewayhttp.ConcurrencyHelper
}

func NewResponsesWSHandler(options ResponsesWSOptions, backend ResponsesWSBackend, concurrency *gatewayhttp.ConcurrencyHelper) *ResponsesWSHandler {
	return &ResponsesWSHandler{options: options, backend: backend, concurrency: concurrency}
}

// ResponsesWebSocket handles authentication checks, ingress leases, upgrade, and the first frame before calling RunEntry.
func (h *ResponsesWSHandler) ResponsesWebSocket(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	if !gatewayhttp.IsResponsesWSUpgrade(c.Request) {
		h.backend.Error(c, http.StatusUpgradeRequired, "invalid_request_error", "WebSocket upgrade required (Upgrade: websocket)")
		return
	}
	h.backend.Transport(c)

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.backend.Error(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.backend.Error(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	reqLog := gatewayhttp.RequestLogger(
		c,
		"handler.openai_gateway.responses_ws",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.Bool("openai_ws_mode", true),
	)
	if !h.backend.Dependencies(c, reqLog) {
		return
	}
	reqLog.Info("openai.websocket_ingress_started")
	clientIP := clientip.GetClientIP(c)
	userAgent := strings.TrimSpace(c.GetHeader("User-Agent"))
	clientLifecycleCtx := c.Request.Context()
	ctx := clientLifecycleCtx
	options := h.options
	if options.Runtime != nil {
		value := options.Runtime.Snapshot()
		options.MaxIngressConnectionsPerAPIKey = value.MaxIngressConnectionsPerAPIKey
		options.ReadLimit = value.ClientReadLimitBytes
		options.FirstMessageTimeout = time.Duration(value.ClientFirstMessageTimeoutSeconds) * time.Second
	}
	maxIngressConnections := options.MaxIngressConnectionsPerAPIKey
	ingressLease, ingressLeaseAcquired, ingressLeaseErr := h.concurrency.AcquireOpenAIWSIngressLease(ctx, apiKey.ID, maxIngressConnections)
	if ingressLeaseErr != nil {
		reqLog.Error("openai.websocket_ingress_lease_acquire_failed", zap.Error(ingressLeaseErr))
		h.backend.Error(c, http.StatusServiceUnavailable, "service_unavailable", "WebSocket ingress capacity is temporarily unavailable")
		return
	}
	if !ingressLeaseAcquired {
		reqLog.Info("openai.websocket_ingress_capacity_rejected", zap.Int("max_ingress_connections_per_api_key", maxIngressConnections))
		c.Header("Retry-After", "5")
		h.backend.Error(c, http.StatusTooManyRequests, "rate_limit_error", "Too many open WebSocket connections, please retry later")
		return
	}
	if ingressLease != nil {
		defer ingressLease.Release()
		ctx = ingressLease.Context()
		c.Request = c.Request.WithContext(ctx)
	}

	wsConn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{
		CompressionMode: coderws.CompressionContextTakeover,
	})
	if err != nil {
		reqLog.Warn("openai.websocket_accept_failed",
			zap.Error(err),
			zap.String("client_ip", clientIP),
			zap.String("request_user_agent", userAgent),
			zap.String("upgrade_header", strings.TrimSpace(c.GetHeader("Upgrade"))),
			zap.String("connection_header", strings.TrimSpace(c.GetHeader("Connection"))),
			zap.String("sec_websocket_version", strings.TrimSpace(c.GetHeader("Sec-WebSocket-Version"))),
			zap.Bool("has_sec_websocket_key", strings.TrimSpace(c.GetHeader("Sec-WebSocket-Key")) != ""),
		)
		return
	}
	defer func() {
		_ = wsConn.CloseNow()
	}()
	wsConn.SetReadLimit(options.ReadLimit)

	firstMessageTimeout := options.FirstMessageTimeout
	msgType, firstMessage, err := gatewayws.ReadClientMessage(
		ctx,
		gatewayhttp.WSClientFrames{Conn: wsConn},
		firstMessageTimeout,
		int(coderws.StatusPolicyViolation),
		"missing first response.create message",
	)
	if err != nil {
		if errors.Is(context.Cause(ctx), scheduler.ErrOpenAIWSIngressLeaseLost) {
			reqLog.Warn("openai.websocket_ingress_lease_lost_before_first_message", zap.Error(err))
			gatewayhttp.CloseResponsesWS(wsConn, coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect")
			return
		}
		closeStatus, closeReason := h.backend.SummarizeRead(err)
		reqLog.Warn("openai.websocket_read_first_message_failed",
			zap.Error(err),
			zap.String("client_ip", clientIP),
			zap.String("close_status", closeStatus),
			zap.String("close_reason", closeReason),
			zap.Duration("read_timeout", firstMessageTimeout),
		)
		gatewayhttp.CloseResponsesWS(wsConn, coderws.StatusPolicyViolation, "missing first response.create message")
		return
	}
	firstTurnStartedAt := time.Now()
	if msgType != int(coderws.MessageText) && msgType != int(coderws.MessageBinary) {
		gatewayhttp.CloseResponsesWS(wsConn, coderws.StatusPolicyViolation, "unsupported websocket message type")
		return
	}
	if !gjson.ValidBytes(firstMessage) {
		gatewayhttp.CloseResponsesWS(wsConn, coderws.StatusPolicyViolation, "invalid JSON payload")
		return
	}

	subjectView := gatewayws.EntrySubject{UserID: subject.UserID, Concurrency: subject.Concurrency}
	ports := h.backend.Entry(c, ResponsesWSCall{Key: apiKey, Subject: subjectView, Conn: wsConn, Logger: reqLog, ClientIP: clientIP, UserAgent: userAgent})
	gatewayws.RunEntry(ctx, ports, gatewayws.EntryInput{Key: apiKey, Subject: subjectView, ClientLifecycleContext: clientLifecycleCtx, FirstTurnStartedAt: firstTurnStartedAt, ClientIP: clientIP, UserAgent: userAgent, MaxProviderSwitches: h.options.MaxProviderSwitches}, gatewayhttp.WSClientFrames{Conn: wsConn}, firstMessage)
}
