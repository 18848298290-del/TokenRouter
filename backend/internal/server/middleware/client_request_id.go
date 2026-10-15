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

// client_request_id.go binds gateway request identities to logging context and
// correlation headers.
// Gateway requests carry a local diagnostic ID and may also carry a caller's
// correlation ID. A valid ID already attached to the request context comes from
// the global RequestLogger or another internal entry point; incoming internal
// response-header names are removed before forwarding. The caller's
// X-Client-Request-ID remains a separate parent value.
//
// ClientRequestID binds these identities to the same request logger and returns
// both current and legacy internal headers with the local value. It also
// retains an existing ingress timestamp and HTTP trace state so gateway stages
// can share the earlier request's timing reference. Header restoration at
// actual response writes belongs to RequestLogger's writer.

const (
	// Correlation header names distinguish the caller's parent ID from the local
	// diagnostic ID exposed under current and legacy product names.
	clientRequestIDHeader         = "X-Client-Request-ID"
	internalRequestIDHeader       = "X-TokenRouter-Request-ID"
	legacyInternalRequestIDHeader = "X-Sub2API-Request-ID"

	// maxPersistentRequestIDBytes bounds accepted correlation text to the stored
	// identifier width used by request records and their searchable caller aliases.
	maxPersistentRequestIDBytes = 64
)

// ClientRequestID binds a local diagnostic ID and a validated caller parent ID
// to each gateway request. Install it after RequestLogger to reuse that entry
// point's ID, or supply a valid telemetry.ClientRequestID context value for an
// internal caller. Missing and invalid context values produce a new UUID.
//
// The middleware removes incoming current and legacy internal headers, then
// exposes its local ID under both response names. A valid X-Client-Request-ID
// is echoed and added to the logger as parent_client_request_id; otherwise the
// response uses the local ID. The incoming caller header stays available to
// downstream handlers, and a nil Request passes through unchanged.
func ClientRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		// Keep the global ingress timestamp when present. Gateway stages then include
		// body upload and scheduling in the same request-relative timing reference.
		ctx := c.Request.Context()
		if _, ok := ctx.Value(telemetry.RequestStartedAt).(time.Time); !ok {
			ctx = context.WithValue(ctx, telemetry.RequestStartedAt, time.Now())
		}
		ctx = timing.WithHTTPTrace(ctx)

		// RequestLogger and gateway middleware share the same telemetry key. Reuse
		// that internal value so retries and downstream diagnostics keep one identity.
		internalID, valid := normalizeCorrelationIDFromContext(ctx, telemetry.ClientRequestID)
		if !valid {
			// An unusable context value cannot identify the stored request. Generate a
			// local UUID before associating the caller's separate parent header.
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
		// These response names identify local diagnostics. Remove both incoming names
		// so an upstream request cannot mistake caller-supplied text for that identity.
		c.Request.Header.Del(internalRequestIDHeader)
		c.Request.Header.Del(legacyInternalRequestIDHeader)
		// Correlation consumers receive the validated parent when present. The local
		// ID supplies a response value when the caller header is missing or unusable.
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

// normalizeCorrelationIDFromContext reads a string correlation value and
// applies the shared normalization rules. Nil contexts, missing values, wrong
// types, and values rejected by normalization return an empty string and false.
func normalizeCorrelationIDFromContext(ctx context.Context, key telemetry.ContextKey) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, _ := ctx.Value(key).(string)
	return normalizeCorrelationID(v)
}

// normalizeCorrelationID returns trimmed, bounded ASCII correlation text and
// whether it is usable. Invalid UTF-8 bytes are removed first; remaining bytes
// must be letters, digits, hyphen, underscore, dot, or colon.
func normalizeCorrelationID(value string) (string, bool) {
	// Remove malformed encoding before checking the persisted identifier. This
	// normalization can retain an otherwise valid ASCII value around discarded
	// bytes.
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if value == "" || len(value) > maxPersistentRequestIDBytes {
		return "", false
	}
	// Request records, structured logs, and response headers share this value. The
	// restricted alphabet keeps control characters and embedded whitespace out of
	// those representations.
	for _, ch := range []byte(value) {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return "", false
	}
	return value, true
}
