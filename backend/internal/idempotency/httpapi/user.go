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

// user.go executes user write commands with actor-scoped fingerprints and
// reports replay or retry state.
// User write fingerprints include the authenticated actor alongside method,
// route, and payload. Records are keyed by the supplied business scope and key
// hash, so a different actor using that same pair produces a fingerprint conflict.
// Responses report stored replays and retry delays, while storage failures return errors.

// ExecuteUserIdempotentJSON runs a user-scoped command through the injected
// coordinator and writes its JSON response. Missing coordinators execute directly
// for fixtures; bound coordinators apply required-key checks according to their
// observe-only configuration and expose stored replays or retry-delay errors.
// Callers provide a nonnil callback for both execution paths.
func (e *Executor) ExecuteUserIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	coordinator := e.coordinator
	if coordinator == nil {
		// Independent HTTP fixtures can omit app binding. Execute their callback directly
		// so these tests retain user response handling without configuring an idempotency repository.
		data, err := execute(c.Request.Context())
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, data)
		return
	}

	actorScope := "user:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "user:" + strconv.FormatInt(subject.UserID, 10)
	}

	result, err := coordinator.Execute(c.Request.Context(), idempotency.IdempotencyExecuteOptions{
		Scope:          scope,
		ActorScope:     actorScope,
		Method:         c.Request.Method,
		Route:          c.FullPath(),
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
		Payload:        payload,
		RequireKey:     true,
		TTL:            ttl,
	}, execute)
	if err != nil {
		if response.ErrorCode(err) == response.ErrorCode(idempotency.ErrIdempotencyStoreUnavail) {
			e.coordinator.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_fail_close")
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=fail_close", c.Request.Method, c.FullPath(), scope)
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
