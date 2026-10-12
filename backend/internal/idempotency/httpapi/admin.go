package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrator write commands identify retries by scope, actor, method, route,
// key, and payload through the shared coordinator. JSON helpers return replay
// and retry-delay headers and return storage errors by default. An administrator
// endpoint can opt into directly executing its callback with a degraded response
// marker when idempotency persistence is unavailable.

// Storage outage modes determine whether the JSON helper returns a store error
// or invokes its direct callback path with a degraded header after persistence fails.
const (
	IdempotencyStoreUnavailableFailClose IdempotencyStoreUnavailableMode = iota // return coordinator storage errors
	IdempotencyStoreUnavailableFailOpen                                         // allow degraded callback execution
)

// IdempotencyStoreUnavailableMode selects administrator callback behavior during a storage outage.
type IdempotencyStoreUnavailableMode int

// ExecuteAdminIdempotent invokes the coordinator using the administrator's actor
// scope and current HTTP request metadata. Keys are requested as required, subject
// to the coordinator's observe-only configuration; a missing coordinator invokes
// the callback directly for standalone HTTP fixtures.
// Callers provide a nonnil callback for both execution paths.
func (e *Executor) ExecuteAdminIdempotent(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) (*idempotency.IdempotencyExecuteResult, error) {
	coordinator := e.coordinator
	if coordinator == nil {
		// Standalone fixtures may lack app binding. Execute their callback directly
		// so handler tests can exercise business responses without constructing persistence dependencies.
		data, err := execute(c.Request.Context())
		if err != nil {
			return nil, err
		}
		return &idempotency.IdempotencyExecuteResult{Data: data}, nil
	}

	return coordinator.Execute(c.Request.Context(), idempotency.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     AdminActorScope(c),
		Method:         c.Request.Method,
		Route:          c.FullPath(),
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		Payload:        payload,
		RequireKey:     true,
		TTL:            ttl,
	}, execute)
}

// AdminActorScope returns the authenticated administrator's idempotency actor label.
// A context without an authentication subject uses admin:0; production routes
// install administrator authentication before invoking helpers that consume this label.
func AdminActorScope(c *gin.Context) string {
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	return actorScope
}

// ExecuteAdminIdempotentJSON runs an administrator callback through the standard
// JSON response helper with storage outages rejected. Replay and retry-delay
// headers come from the coordinator result or its structured error metadata.
func (e *Executor) ExecuteAdminIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	e.ExecuteAdminIdempotentJSONWithMode(c, scope, payload, ttl, IdempotencyStoreUnavailableFailClose, execute)
}

// ExecuteAdminIdempotentJSONWithMode runs an administrator command and writes its
// JSON response, marking stored-response replays and forwarding retry-delay metadata.
// IdempotencyStoreUnavailableFailOpen invokes the callback directly when the
// coordinator reports store unavailability and marks a successful degraded response.
//
// A store error can also arise while persisting a callback's successful result.
// Endpoints selecting fail-open therefore need to tolerate callback re-execution
// in that situation. The default mode returns the error and leaves that decision
// to the endpoint's caller.
func (e *Executor) ExecuteAdminIdempotentJSONWithMode(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	mode IdempotencyStoreUnavailableMode,
	execute func(context.Context) (any, error),
) {
	result, err := e.ExecuteAdminIdempotent(c, scope, payload, ttl, execute)
	if err != nil {
		if response.ErrorCode(err) == response.ErrorCode(idempotency.ErrIdempotencyStoreUnavail) {
			strategy := "fail_close"
			if mode == IdempotencyStoreUnavailableFailOpen {
				strategy = "fail_open"
			}
			e.coordinator.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_"+strategy)
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=%s", c.Request.Method, c.FullPath(), scope, strategy)
			if mode == IdempotencyStoreUnavailableFailOpen {
				// This endpoint selected degraded execution. Invoke the callback with the
				// request context and mark success so clients can see that persistence was unavailable.
				data, fallbackErr := execute(c.Request.Context())
				if fallbackErr != nil {
					response.ErrorFrom(c, fallbackErr)
					return
				}
				c.Header("X-Idempotency-Degraded", "store-unavailable")
				response.Success(c, data)
				return
			}
		}
		if retryAfter := idempotency.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}
	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}
