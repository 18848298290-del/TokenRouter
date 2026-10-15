package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
)

// ws_errors.go presents Responses WebSocket failures as error events or close
// instructions and records their intended HTTP status.
// An upgraded Responses connection cannot receive a new HTTP status line when
// execution fails. Errors become protocol text events or WebSocket close
// instructions, while Ops records the intended HTTP status separately;
// permission, capacity and service failures retain distinct presentation.
//
// Credential failures take precedence over upstream status when choosing the
// final close instruction. Otherwise rate limits and temporary server failures
// request a later retry, while authentication statuses select a policy
// violation. Moderation and session-isolation paths write bounded-time text
// events on an existing connection. The termination classifier considers
// explicit normal closure and cancellation separately from error presentation
// so ingress can attribute the connection ending.

// ResponsesWSFailure holds the error details shown to the client after a WebSocket upgrade.
type ResponsesWSFailure struct {
	// Reason replaces the default error code when its trimmed value is nonblank.
	// Empty text leaves the exhausted-failover code available for client diagnostics.
	Reason string
	// ProviderAuth selects CredentialMessage and the retry close instruction;
	// otherwise StatusCode selects the rate, service or authentication presentation.
	StatusCode        int    // observed upstream HTTP status
	ProviderAuth      bool   // credential failure classification
	CredentialMessage string // safe credential client message
}

// CloseResponsesWSFailure records exhausted upstream failure details and closes
// an upgraded client connection. The mark callback is required and runs before
// closure. Credential, rate-limit, temporary-service and authentication
// failures choose separate intended HTTP observations and WebSocket close
// codes.
func CloseResponsesWSFailure(c *gin.Context, conn *coderws.Conn, failoverErr *ResponsesWSFailure, mark func(*gin.Context, string, string, string, int)) {
	intendedStatus := http.StatusBadGateway
	errorType := "upstream_error"
	errorCode := "upstream_ws_failover_exhausted"
	message := "upstream websocket proxy failed"
	closeStatus := coderws.StatusInternalError

	if failoverErr != nil {
		if reason := strings.TrimSpace(failoverErr.Reason); reason != "" {
			errorCode = reason
		}
		if failoverErr.ProviderAuth {
			intendedStatus = http.StatusServiceUnavailable
			errorType = "api_error"
			message = failoverErr.CredentialMessage
			closeStatus = coderws.StatusTryAgainLater
		} else {
			switch failoverErr.StatusCode {
			case http.StatusTooManyRequests:
				intendedStatus = http.StatusTooManyRequests
				errorType = "rate_limit_error"
				message = "upstream rate limit exceeded, please retry later"
				closeStatus = coderws.StatusTryAgainLater
			case 529, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				intendedStatus = failoverErr.StatusCode
				message = "upstream service temporarily unavailable"
				closeStatus = coderws.StatusTryAgainLater
			case http.StatusUnauthorized, http.StatusForbidden:
				intendedStatus = failoverErr.StatusCode
				errorType = "authentication_error"
				message = "upstream websocket authentication failed"
				closeStatus = coderws.StatusPolicyViolation
			}
		}
	}

	// Record the intended HTTP failure before transport closure because the
	// upgraded response retains its earlier switching-protocols status line.
	mark(c, errorType, errorCode, message, intendedStatus)
	CloseResponsesWS(conn, closeStatus, message)
}

// WriteResponsesWSModeration attempts a text error event for a blocking
// moderation decision. Nil connections or decisions skip output, nil context
// uses Background, and blank messages use the shared moderation text. Writes
// have a two-second budget and errors are discarded.
func WriteResponsesWSModeration(ctx context.Context, conn *coderws.Conn, decision *moderation.Decision) {
	if conn == nil || decision == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	message := strings.TrimSpace(decision.Message)
	if message == "" {
		message = "content moderation blocked this request"
	}
	payload, err := json.Marshal(gin.H{
		"event_id": "evt_content_moderation_blocked",
		"type":     "error",
		"error": gin.H{
			"type":    "invalid_request_error",
			"code":    "content_policy_violation",
			"message": message,
		},
	})
	// Retain a fixed valid moderation event if encoding fails, so a blocked
	// request still has a protocol error available for the bounded write.
	if err != nil {
		payload = []byte(`{"event_id":"evt_content_moderation_blocked","type":"error","error":{"type":"invalid_request_error","code":"content_policy_violation","message":"content moderation blocked this request"}}`)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Delivery is best effort within this write budget; a disconnected client
	// cannot prevent the caller from finishing its rejection or teardown path.
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

// WriteResponsesWSCyberBlocked attempts a permission error event carrying the
// supplied Cyber message. Nil connections skip output and nil context uses
// Background. The write has a two-second budget; callers decide subsequent
// closure because this helper discards write errors.
func WriteResponsesWSCyberBlocked(ctx context.Context, conn *coderws.Conn, message string) {
	if conn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(gin.H{
		"event_id": "evt_cyber_session_blocked",
		"type":     "error",
		"error": gin.H{
			"type":    "permission_error",
			"code":    "session_blocked_by_cyber_policy",
			"message": message,
		},
	})
	// Use the shared safe Cyber rejection text if encoding fails, keeping the
	// fixed event shape available without the caller-supplied dynamic message.
	if err != nil {
		payload = []byte(`{"event_id":"evt_cyber_session_blocked","type":"error","error":{"type":"permission_error","code":"session_blocked_by_cyber_policy","message":"This session is blocked by cyber-security policy, please start a new session"}}`)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Delivery is best effort within this write budget; a disconnected client
	// cannot prevent the caller from finishing its rejection or teardown path.
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

// WriteResponsesWSIsolation reports whether an isolation error requires
// handling and attempts its text error event. Nil errors return false; nil
// connections still return true. Conflicts use the permission message, other
// failures use a safe service error, and write or marshal failure does not
// change the handled result.
func WriteResponsesWSIsolation(ctx context.Context, conn *coderws.Conn, err error) bool {
	if err == nil {
		return false
	}
	if conn == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	message := "Service temporarily unavailable"
	code := "service_unavailable"
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		message = session.SessionIsolationConflictMessage
		code = "permission_error"
	} else {
		closeMessage := message
		payload, marshalErr := json.Marshal(gin.H{
			"event_id": "evt_session_isolation_cache_failed",
			"type":     "error",
			"error": gin.H{
				"type":    "api_error",
				"code":    code,
				"message": closeMessage,
			},
		})
		if marshalErr == nil {
			writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			// Delivery is best effort within this write budget; a disconnected client
			// cannot prevent the caller from finishing its rejection or teardown path.
			_ = conn.Write(writeCtx, coderws.MessageText, payload)
			return true
		}
	}
	// A conflict, or failed service-event encoding, reaches this permission-shaped
	// envelope using the chosen message and code before the final fixed fallback.
	payload, marshalErr := json.Marshal(gin.H{
		"event_id": "evt_session_isolation_blocked",
		"type":     "error",
		"error": gin.H{
			"type":    "permission_error",
			"code":    code,
			"message": message,
		},
	})
	// A failed encoding attempt falls back to the fixed conflict-shaped event
	// so this handled isolation error still has a bounded delivery attempt.
	if marshalErr != nil {
		payload = []byte(`{"event_id":"evt_session_isolation_blocked","type":"error","error":{"type":"permission_error","code":"permission_error","message":"` + strings.ReplaceAll(session.SessionIsolationConflictMessage, `"`, `\"`) + `"}}`)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Delivery is best effort within this write budget; a disconnected client
	// cannot prevent the caller from finishing its rejection or teardown path.
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
	return true
}

// ResponsesWSIsolationCloseReason chooses safe close text for an isolation
// result. A wrapped conflict uses the shared conflict message; every other
// input, including nil, uses the generic isolation-check failure reason.
func ResponsesWSIsolationCloseReason(err error) string {
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		return session.SessionIsolationConflictMessage
	}
	return "session isolation check failed"
}

// ResponsesWSEndedByClient attributes nil errors, normal-close evidence or
// wrapped cancellation to a client end. It accepts both gateway close
// information and library close codes; abnormal closure alone and deadline
// expiry without normal-close evidence remain failures.
func ResponsesWSEndedByClient(err error, info gatewayws.EntryClose) bool {
	if err == nil {
		return true
	}
	if info.Present && info.Status == int(coderws.StatusNormalClosure) {
		return true
	}
	if coderws.CloseStatus(err) == coderws.StatusNormalClosure {
		return true
	}
	return errors.Is(err, context.Canceled)
}
