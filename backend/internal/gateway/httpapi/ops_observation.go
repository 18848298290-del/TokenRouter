package httpapi

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

const (
	// Gin context keys used by Ops error logger for capturing upstream error details.
	// Gateway adapters set these keys; OpsErrorLoggerMiddleware reads them.
	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"
	OpsUpstreamModelKey        = "ops_upstream_model"

	// Optional stage latencies (milliseconds) for troubleshooting and alerting.
	OpsAuthLatencyMsKey      = "ops_auth_latency_ms"
	OpsRoutingLatencyMsKey   = "ops_routing_latency_ms"
	OpsUpstreamLatencyMsKey  = "ops_upstream_latency_ms"
	OpsResponseLatencyMsKey  = "ops_response_latency_ms"
	OpsTimeToFirstTokenMsKey = "ops_time_to_first_token_ms"
	// OpenAI WebSocket timing and connection observations.
	OpsOpenAIWSQueueWaitMsKey = "ops_openai_ws_queue_wait_ms"
	OpsOpenAIWSConnPickMsKey  = "ops_openai_ws_conn_pick_ms"
	OpsOpenAIWSConnReusedKey  = "ops_openai_ws_conn_reused"
	OpsOpenAIWSConnIDKey      = "ops_openai_ws_conn_id"

	// OpsSkipPassthroughKey records a matched skip_monitoring=true passthrough rule.
	// The error logger checks this flag before recording the final failure.
	OpsSkipPassthroughKey = "ops_skip_passthrough"

	// OpsStreamErrorKey stores an in-band error after HTTP status commitment.
	// The error logger reads this marker when the wire status remains below 400.
	// Examples include concurrency wait timeouts and funds recheck failures
	// after a heartbeat has already committed HTTP 200.
	OpsStreamErrorKey  = "ops_stream_error"
	OpsStreamErrorsKey = "ops_stream_errors"
	OpsStreamTurnKey   = "ops_stream_turn"

	// ResponseCommittedKey prevents later error writers from emitting another response.
	// Error handlers set it after writing the client error.
	ResponseCommittedKey = "response_committed"

	// Client business restrictions are logged but excluded from SLA and error-rate statistics.
	OpsClientBusinessLimitedKey                           = "ops_client_business_limited"
	OpsClientBusinessLimitedReasonKey                     = "ops_client_business_limited_reason"
	OpsClientBusinessLimitedReasonIPRestriction           = "api_key_ip_restriction"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable  = "api_key_group_unavailable"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned   = "api_key_group_unassigned"
	OpsClientBusinessLimitedReasonLocalFeatureGate        = "local_feature_gate"
	OpsClientBusinessLimitedReasonLocalPolicyDenied       = "local_policy_denied"
	OpsClientBusinessLimitedReasonLocalModelConfiguration = "local_model_configuration"

	maxOpsStreamErrorsPerRequest = 64
)

// OpsStreamError describes a client-visible failure after stream commitment.
// A keepalive or partial output can leave the HTTP status at 200 despite failure.
// The error logger uses the intended status and error details to record that failure.
// Examples include concurrency limits, funds rechecks after waiting,
// and exhausted provider selection after output starts.
// WebSocket turns retain separate snapshots; HTTP/SSE uses turn zero.
type OpsStreamError struct {
	// ErrType is the client-facing error type, such as rate_limit_error or upstream_error.
	ErrType string
	// Code is an optional stable classification, such as upstream_http2_stream_error,
	// for clients and Ops to distinguish failures within a general error type.
	Code string
	// Message is the client-facing error message.
	Message string
	// IntendedStatus is the HTTP status the error would use before commitment, such as 429.
	// It controls severity and, when CountTowardsSLA is true, the logical Ops status.
	IntendedStatus int
	// CountTowardsSLA uses IntendedStatus for error-rate and SLA accounting after commitment.
	CountTowardsSLA bool
	// Turn identifies a WebSocket turn. HTTP/SSE requests leave it at zero.
	Turn int
	// SkipMonitoring snapshots the rule decision for this visible failure.
	SkipMonitoring  bool
	ProviderID      int64
	UpstreamModel   string
	UpstreamStatus  int
	UpstreamMessage string
	UpstreamDetail  string
	UpstreamErrors  []*ops.OpsUpstreamErrorEvent
}

func MarkResponseCommitted(c *gin.Context) { c.Set(ResponseCommittedKey, true) }

func IsResponseCommitted(c *gin.Context) bool {
	v, ok := c.Get(ResponseCommittedKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func SetOpsLatencyMs(c *gin.Context, key string, value int64) {
	if c == nil || strings.TrimSpace(key) == "" || value < 0 {
		return
	}
	c.Set(key, value)
}

// MarkOpsTimestamp records the first absolute stage time for access-log latency calculation.
func MarkOpsTimestamp(c *gin.Context, key telemetry.ContextKey) {
	if c == nil || c.Request == nil || key == "" {
		return
	}
	ctx := c.Request.Context()
	if _, exists := ctx.Value(key).(time.Time); exists {
		return
	}
	ctx = context.WithValue(ctx, key, time.Now())
	c.Request = c.Request.WithContext(ctx)
}

// MarkOpsProviderSlotAcquired records successful provider slot acquisition.
func MarkOpsProviderSlotAcquired(c *gin.Context) {
	MarkOpsTimestamp(c, telemetry.ProviderSlotAcquiredAt)
}

// SetOpsUpstreamModel stores only the effective model slug for final Ops
// attribution. Call it immediately before an upstream attempt is dispatched.
func SetOpsUpstreamModel(c *gin.Context, model string) {
	if c == nil {
		return
	}
	if model = strings.TrimSpace(model); model != "" {
		c.Set(OpsUpstreamModelKey, model)
	}
}

// ClearOpsUpstreamModel invalidates attempt-scoped model attribution before a
// newly selected provider starts credential resolution or upstream dispatch.
func ClearOpsUpstreamModel(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(OpsUpstreamModelKey, "")
}

func MarkOpsClientBusinessLimited(c *gin.Context, reason string) {
	if c == nil {
		return
	}
	c.Set(OpsClientBusinessLimitedKey, true)
	if reason = strings.TrimSpace(reason); reason != "" {
		c.Set(OpsClientBusinessLimitedReasonKey, reason)
	}
}

// HasOpsClientBusinessLimited reports whether the request has a client-side business restriction.
func HasOpsClientBusinessLimited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientBusinessLimitedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

func OpsClientBusinessLimitedReason(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, ok := c.Get(OpsClientBusinessLimitedReasonKey)
	if !ok {
		return ""
	}
	reason, _ := v.(string)
	return strings.TrimSpace(reason)
}

// BeginOpsStreamTurn scopes first-wins deduplication to one WebSocket turn.
func BeginOpsStreamTurn(c *gin.Context, turn int) {
	if c == nil || turn <= 0 {
		return
	}
	c.Set(OpsStreamTurnKey, turn)
	// Rule and attempt state is turn-scoped on a long-lived WS connection.
	c.Set(OpsSkipPassthroughKey, false)
	c.Set(OpsUpstreamErrorsKey, []*ops.OpsUpstreamErrorEvent{})
	c.Set(OpsUpstreamStatusCodeKey, 0)
	c.Set(OpsUpstreamErrorMessageKey, "")
	c.Set(OpsUpstreamErrorDetailKey, "")
}

// MarkOpsStreamError records an in-band error for Ops logging.
// The first marker wins within an HTTP request or WebSocket turn, including over later generic failures.
func MarkOpsStreamError(c *gin.Context, errType, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:        errType,
		Message:        message,
		IntendedStatus: intendedStatus,
	})
}

// MarkOpsStreamFailure records a stream failure for error-rate and SLA accounting after status commitment.
func MarkOpsStreamFailure(c *gin.Context, errType, code, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:         errType,
		Code:            code,
		Message:         message,
		IntendedStatus:  intendedStatus,
		CountTowardsSLA: true,
	})
}

func markOpsStreamError(c *gin.Context, streamErr OpsStreamError) {
	if c == nil {
		return
	}
	if streamErr.CountTowardsSLA && c.Request != nil {
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) { record.State = "failed"; record.ErrorCode = "stream_error" })
	}
	streamErr.ErrType = strings.TrimSpace(streamErr.ErrType)
	streamErr.Code = strings.TrimSpace(streamErr.Code)
	streamErr.Message = strings.TrimSpace(streamErr.Message)
	streamErr.SkipMonitoring = currentOpsFailureSkipMonitoring(c)
	snapshotOpsStreamErrorContext(c, &streamErr)
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		if value, ok := c.Get(OpsStreamTurnKey); ok {
			streamErr.Turn, _ = value.(int)
		}
		var errorsForRequest []OpsStreamError
		if value, ok := c.Get(OpsStreamErrorsKey); ok {
			errorsForRequest, _ = value.([]OpsStreamError)
		}
		if len(errorsForRequest) > 0 && errorsForRequest[len(errorsForRequest)-1].Turn == streamErr.Turn {
			return
		}
		errorsForRequest = append(errorsForRequest, streamErr)
		if len(errorsForRequest) > maxOpsStreamErrorsPerRequest {
			errorsForRequest = append([]OpsStreamError(nil), errorsForRequest[len(errorsForRequest)-maxOpsStreamErrorsPerRequest:]...)
		}
		c.Set(OpsStreamErrorsKey, errorsForRequest)
		c.Set(OpsStreamErrorKey, streamErr)
		return
	}
	if _, exists := c.Get(OpsStreamErrorKey); exists {
		return
	}
	c.Set(OpsStreamErrorKey, streamErr)
}

func snapshotOpsStreamErrorContext(c *gin.Context, streamErr *OpsStreamError) {
	if c == nil || streamErr == nil {
		return
	}
	if c.Request != nil {
		if providerID, ok := c.Request.Context().Value(telemetry.ProviderID).(int64); ok && providerID > 0 {
			streamErr.ProviderID = providerID
		}
	}
	if value, ok := c.Get(OpsUpstreamModelKey); ok {
		streamErr.UpstreamModel, _ = value.(string)
		streamErr.UpstreamModel = strings.TrimSpace(streamErr.UpstreamModel)
	}
	if value, ok := c.Get(OpsUpstreamStatusCodeKey); ok {
		switch status := value.(type) {
		case int:
			streamErr.UpstreamStatus = status
		case int64:
			streamErr.UpstreamStatus = int(status)
		}
	}
	if value, ok := c.Get(OpsUpstreamErrorMessageKey); ok {
		streamErr.UpstreamMessage, _ = value.(string)
		streamErr.UpstreamMessage = strings.TrimSpace(streamErr.UpstreamMessage)
	}
	if value, ok := c.Get(OpsUpstreamErrorDetailKey); ok {
		streamErr.UpstreamDetail, _ = value.(string)
		streamErr.UpstreamDetail = strings.TrimSpace(streamErr.UpstreamDetail)
	}
	if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := value.([]*ops.OpsUpstreamErrorEvent); ok {
			streamErr.UpstreamErrors = make([]*ops.OpsUpstreamErrorEvent, 0, len(events))
			for _, event := range events {
				if event == nil {
					streamErr.UpstreamErrors = append(streamErr.UpstreamErrors, nil)
					continue
				}
				copyOfEvent := *event
				streamErr.UpstreamErrors = append(streamErr.UpstreamErrors, &copyOfEvent)
			}
		}
	}
}

func currentOpsFailureSkipMonitoring(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if value, ok := c.Get(OpsSkipPassthroughKey); ok {
		if skip, _ := value.(bool); skip {
			return true
		}
	}
	if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := value.([]*ops.OpsUpstreamErrorEvent); ok {
			for _, event := range slices.Backward(events) {
				if event != nil {
					return event.SkipMonitoring
				}
			}
		}
	}
	return false
}

// GetOpsStreamError returns the current in-band error marker, if any.
func GetOpsStreamError(c *gin.Context) (OpsStreamError, bool) {
	if c == nil {
		return OpsStreamError{}, false
	}
	v, ok := c.Get(OpsStreamErrorKey)
	if !ok {
		return OpsStreamError{}, false
	}
	se, ok := v.(OpsStreamError)
	return se, ok
}

func GetOpsStreamErrors(c *gin.Context) []OpsStreamError {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(OpsStreamErrorsKey); ok {
		if errorsForRequest, ok := value.([]OpsStreamError); ok && len(errorsForRequest) > 0 {
			return append([]OpsStreamError(nil), errorsForRequest...)
		}
	}
	if streamErr, ok := GetOpsStreamError(c); ok {
		return []OpsStreamError{streamErr}
	}
	return nil
}

// SetOpsUpstreamError records actual upstream status and sanitized details for HTTP capture.
func SetOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	if c == nil {
		return
	}
	if upstreamStatusCode > 0 {
		c.Set(OpsUpstreamStatusCodeKey, upstreamStatusCode)
	}
	if msg := strings.TrimSpace(upstreamMessage); msg != "" {
		c.Set(OpsUpstreamErrorMessageKey, msg)
	}
	if detail := strings.TrimSpace(upstreamDetail); detail != "" {
		c.Set(OpsUpstreamErrorDetailKey, detail)
	}
}

// AppendOpsUpstreamError snapshots an attempt and its rule match for later error logging.
func AppendOpsUpstreamError(c *gin.Context, ev ops.OpsUpstreamErrorEvent) {
	if c == nil {
		return
	}
	if ev.AtUnixMs <= 0 {
		ev.AtUnixMs = time.Now().UnixMilli()
	}
	ev.Platform = strings.TrimSpace(ev.Platform)
	ev.UpstreamRequestID = strings.TrimSpace(ev.UpstreamRequestID)
	ev.UpstreamResponseBody = strings.TrimSpace(ev.UpstreamResponseBody)
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.Stage = strings.TrimSpace(ev.Stage)
	ev.Scope = strings.TrimSpace(ev.Scope)
	ev.Reason = strings.TrimSpace(ev.Reason)
	ev.UpstreamURL = strings.TrimSpace(ev.UpstreamURL)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.Detail = strings.TrimSpace(ev.Detail)
	if ev.Message != "" {
		ev.Message = logredact.SanitizeUpstreamQueries(ev.Message)
	}

	var existing []*ops.OpsUpstreamErrorEvent
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if arr, ok := v.([]*ops.OpsUpstreamErrorEvent); ok {
			existing = arr
		}
	}

	if c.Request != nil {
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) {
			if len(record.Attempts) < 256 {
				outcome := "failed"
				if ev.Kind == "usage_invalid" {
					outcome = "usage_unknown"
				}
				record.Attempts = append(record.Attempts, telemetry.RequestAttempt{Number: len(record.Attempts) + 1, ProviderID: ev.ProviderID, RequestID: ev.UpstreamRequestID, Status: ev.UpstreamStatusCode, Outcome: outcome})
			}
		})
		telemetry.AddRequestAlias(c.Request.Context(), "upstream", ev.UpstreamRequestID)
	}
	evCopy := ev
	existing = append(existing, &evCopy)
	c.Set(OpsUpstreamErrorsKey, existing)

	checkSkipMonitoringForUpstreamEvent(c, &evCopy)
}

// checkSkipMonitoringForUpstreamEvent snapshots whether this attempt matches a
// skip_monitoring passthrough rule. The final failure decides whether the
// request error is hidden; an intermediate recovered attempt cannot suppress a
// later client-visible failure.
func checkSkipMonitoringForUpstreamEvent(c *gin.Context, ev *ops.OpsUpstreamErrorEvent) {
	if ev.UpstreamStatusCode == 0 {
		return
	}

	svc := BoundErrorPassthroughService(c)
	if svc == nil {
		return
	}

	// Use the best available body representation for keyword matching.
	// Even when body is empty, MatchRule can still match rules that only
	// specify ErrorCodes (no Keywords), so we always call it.
	body := ev.Detail
	if body == "" {
		body = ev.Message
	}

	rule := svc.MatchRule(ev.Platform, ev.UpstreamStatusCode, []byte(body))
	if rule != nil && rule.SkipMonitoring {
		ev.SkipMonitoring = true
	}
}
