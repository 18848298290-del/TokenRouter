package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/systemone"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/systemone"
)

// systemone_handler.go admits SystemOne HTTP requests and passes parsed
// decision input to the shared execution loop.
// SystemOne accepts synchronous structured decision input and reuses shared
// authentication, moderation and concurrency admission. Its HTTP handler
// passes a validated model/body and mapping into the decision loop, then
// leaves provider retries and completion qualification to systemone.Run before
// final error presentation.

// SystemOneHTTPExecution runs a decision request and writes its final error.
type SystemOneHTTPExecution interface {
	systemone.Ports // serial decision execution operations
	// End presents the final execution error when HTTP output is still available.
	// The request owner invokes it after systemone.Run, including nil on success,
	// so already delivered answers remain the final response.
	End(error)
}

// SystemOneHTTPPorts supplies the request factory and content moderation for SystemOne.
type SystemOneHTTPPorts interface {
	// NewSystemOne captures request identity, validated input and output state
	// for the decision loop. The returned execution retains selected-provider
	// state across its serial operations and is ended after the loop returns.
	NewSystemOne(*gin.Context, AuxiliaryHTTPInput, *zap.Logger, *bool) SystemOneHTTPExecution
	// ModerateSystemOne checks extracted structured content and reports whether
	// it rejected the request. True means client error output has been presented,
	// so the handler stops before concurrency and provider execution.
	ModerateSystemOne(*gin.Context, *zap.Logger, string, []byte) bool
}

// SystemOne validates synchronous decision input and performs shared request
// admission before provider execution. Middleware supplies key and subject
// state; moderation sees extracted content before user-slot acquisition, and
// funding is checked again after that wait. The acquired user release is
// deferred until request handling ends.
func (h *AuxiliaryHandler) SystemOne(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "systemone")
	if !accepted {
		return
	}
	defer done()
	started := time.Now()
	stream := false
	access, ok := h.ports.Access(c)
	if !ok {
		h.ports.Error(c, 401, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := h.ports.Subject(c)
	if !ok {
		h.ports.Error(c, 500, "api_error", "User context not found")
		return
	}
	log := h.ports.Logger(c, "handler.systemone", zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", access.ID))
	ports, ok := h.ports.(SystemOneHTTPPorts)
	if !ok {
		h.ports.Error(c, 503, "api_error", "SystemOne is not configured")
		return
	}
	if !h.ports.Dependencies(c, log) {
		return
	}
	h.ports.BindErrors(c)
	body, ok := h.base.readBody(c)
	if !ok {
		return
	}
	request, err := wire.ParseRequest(body)
	if err != nil {
		h.ports.Error(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	h.ports.ObserveRequest(c, request.Model, false, true)
	_, mapping := h.ports.Plan(c, request.Model, true)
	if ports.ModerateSystemOne(c, log, request.Model, request.ModerationBody()) {
		return
	}
	h.ports.AuthLatency(c, time.Since(started))
	release, ok := h.ports.AcquireUser(c, subject, false, &stream, log)
	if !ok {
		return
	}
	if release != nil {
		defer release()
	}
	if failure := h.ports.Billing(c); failure != nil {
		if failure.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(failure.RetryAfter))
		}
		h.ports.Error(c, failure.Status, failure.Code, failure.Message)
		return
	}
	run := ports.NewSystemOne(c, AuxiliaryHTTPInput{Subject: subject, Model: request.Model, Body: body, Mapping: mapping}, log, &stream)
	run.End(systemone.Run(c.Request.Context(), h.ports.MaxSwitches(), run))
}

// IsSystemOneEndpoint reports whether a path is exactly the SystemOne endpoint
// after removing one trailing slash. Additional path segments or another
// remaining slash do not match this endpoint predicate.
func IsSystemOneEndpoint(path string) bool {
	return strings.TrimSuffix(path, "/") == EndpointSystemOne
}

// WriteSystemOneError writes the SystemOne gateway error envelope while the
// response is unwritten. Empty code and param values are omitted; once a
// response was written, this helper returns without adding another error body.
func WriteSystemOneError(c *gin.Context, status int, kind, code, param, message string) {
	if c.Writer.Written() {
		return
	}
	value := gin.H{"type": kind, "message": message}
	if code != "" {
		value["code"] = code
	}
	if param != "" {
		value["param"] = param
	}
	c.JSON(status, gin.H{"error": value})
}
