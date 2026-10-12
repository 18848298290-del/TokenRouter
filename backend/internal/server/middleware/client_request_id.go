package middleware

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

const (
	clientRequestIDHeader         = "X-Client-Request-ID"
	internalRequestIDHeader       = "X-TokenRouter-Request-ID"
	legacyInternalRequestIDHeader = "X-Sub2API-Request-ID"

	maxPersistentRequestIDBytes = 64
)

// ClientRequestID assigns an internal request ID and stores the caller ID as parent_client_request_id.
// The caller ID correlates services; it cannot choose the internal request ID.
func ClientRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		// Record ingress time before body reads and scheduling to distinguish upload and application latency.
		ctx := c.Request.Context()
		if _, ok := ctx.Value(telemetry.RequestStartedAt).(time.Time); !ok {
			ctx = context.WithValue(ctx, telemetry.RequestStartedAt, time.Now())
		}
		ctx = timing.WithHTTPTrace(ctx)

		// Reuse a valid context ID supplied by trusted internal callers.
		internalID, valid := normalizeCorrelationIDFromContext(ctx, telemetry.ClientRequestID)
		if !valid {
			internalID = telemetry.NewRequestID()
		}
		parentID, _ := normalizeCorrelationID(c.GetHeader(clientRequestIDHeader))
		ctx = context.WithValue(ctx, telemetry.ClientRequestID, internalID)
		if parentID != "" {
			ctx = context.WithValue(ctx, telemetry.ParentClientRequestID, parentID)
		}
		requestLogger := logging.FromContext(ctx).With(zap.String("request_id", internalID))
		if parentID != "" {
			requestLogger = requestLogger.With(zap.String("parent_client_request_id", parentID))
		}
		ctx = logging.IntoContext(ctx, requestLogger)
		c.Request = c.Request.WithContext(ctx)
		// Remove caller-supplied internal ID headers before downstream forwarding.
		c.Request.Header.Del(internalRequestIDHeader)
		c.Request.Header.Del(legacyInternalRequestIDHeader)
		// Echo a valid caller ID, or use the internal ID when none was supplied.
		if parentID != "" {
			c.Header(clientRequestIDHeader, parentID)
		} else {
			c.Header(clientRequestIDHeader, internalID)
		}
		// Dedicated response headers expose the internal diagnostic ID.
		c.Header(internalRequestIDHeader, internalID)
		// The legacy header carries the same ID for older clients.
		c.Header(legacyInternalRequestIDHeader, internalID)
		c.Next()
	}
}

func normalizeCorrelationIDFromContext(ctx context.Context, key telemetry.ContextKey) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, _ := ctx.Value(key).(string)
	return normalizeCorrelationID(v)
}

// normalizeCorrelationID trims and validates the ID's byte length and ASCII characters.
func normalizeCorrelationID(value string) (string, bool) {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if value == "" || len(value) > maxPersistentRequestIDBytes {
		return "", false
	}
	// Restrict untrusted IDs to characters safe for logs and HTTP headers.
	for _, ch := range []byte(value) {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return "", false
	}
	return value, true
}
