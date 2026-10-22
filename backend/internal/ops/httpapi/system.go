package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idemhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/ops/maintenance"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// system.go serves release queries and submits update, rollback, and restart
// commands with operation identities.
// System maintenance commands pair an HTTP idempotency payload with a separate
// maintenance operation identity. A supplied key derives that identity from the
// operation, administrator subject, and registered route; an absent key uses a
// nanosecond timestamp suffix. The hash is shortened for the visible identity
// and supplies no collision-free or timestamp-unique guarantee.
//
// Version and candidate reads call the update service directly, while update,
// rollback, and restart commands go through the retained Operations instance.
// App wiring binds HTTP idempotency separately; a zero Executor used by
// fixtures runs callbacks directly. Rollback treats an omitted or empty target
// as local-backup restoration and includes a nonempty target in both operation
// identity and payload before downstream eligibility validation.

// SystemHandler adapts update reads and leased maintenance commands to
// administrator HTTP responses.
type SystemHandler struct {
	// Executor is bound by production app wiring before requests. Its zero value
	// executes command callbacks directly for standalone fixtures, independently
	// of Operations' maintenance leases.
	idemhttp.Executor // HTTP idempotency command helper
	// updateSvc supplies version and rollback-candidate reads. GetVersion ignores
	// its error and requires a nonnil UpdateInfo result before reading
	// CurrentVersion.
	updateSvc systemUpdateService // retained update query dependency
	// operations executes update, rollback, and restart commands with its own
	// lifecycle and lease handling. Callers supply it before serving writes.
	operations *maintenance.Operations // retained maintenance command runtime
}

// systemUpdateService is the update query and executable-maintenance API
// retained by the system HTTP adapter.
type systemUpdateService interface {
	// CheckUpdate provides display versions under its force/cache policy.
	// GetVersion reads the returned pointer even when an error is present, so that
	// caller requires a nonnil result for its version response.
	CheckUpdate(ctx context.Context, force bool) (*ops.UpdateInfo, error)
	// PerformUpdate applies an available release using its supplied operation
	// context and reports installation failure or the no-update sentinel.
	// SystemHandler submits writes through Operations, whose update dependency
	// supplies this executable method.
	PerformUpdate(ctx context.Context) error
	// Rollback restores a retained local executable and reports the installer's
	// result. The method has no context argument; the HTTP adapter selects this
	// path through Operations when target version is empty.
	Rollback() error
	// ListRollbackVersions returns display information for allowed downloadable
	// rollback candidates. The handler forwards request context and uses those
	// entries as response data; selection and candidate errors belong to the
	// update service.
	ListRollbackVersions(ctx context.Context) ([]ops.RollbackVersion, error)
	// RollbackToVersion validates and installs a selected release under the
	// supplied context. The HTTP adapter trims the target first and delegates this
	// path through Operations, which supplies the accepted operation lifetime.
	RollbackToVersion(ctx context.Context, version string) error
}

// NewSystemRuntimeHandler retains the supplied update reader and maintenance
// runtime without starting work. Callers provide both dependencies and bind the
// embedded Executor before production requests; the constructor itself performs
// no nil validation.
func NewSystemRuntimeHandler(update systemUpdateService, operations *maintenance.Operations) *SystemHandler {
	return &SystemHandler{updateSvc: update, operations: operations}
}

// GetVersion queries updateSvc with force false, discards its error, and
// returns CurrentVersion in the success payload. Its dependency must supply a
// nonnil UpdateInfo even for an error result; a nil pointer is dereferenced
// here.
// GET /api/v1/admin/system/version
func (h *SystemHandler) GetVersion(c *gin.Context) {
	// Discard the query error and read its display result directly; this response
	// path depends on updateSvc returning nonnil information.
	info, _ := h.updateSvc.CheckUpdate(c.Request.Context(), false)
	response.Success(c, gin.H{
		"version": info.CurrentVersion,
	})
}

// CheckUpdates returns update information or the query's error text as an
// internal-server response. force is true only for the exact query spelling
// true; whitespace, different case, and other truth-like text do not enable it.
// GET /api/v1/admin/system/check-updates
func (h *SystemHandler) CheckUpdates(c *gin.Context) {
	force := c.Query("force") == "true"
	info, err := h.updateSvc.CheckUpdate(c.Request.Context(), force)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, info)
}

// PerformUpdate builds an update identity and submits its operation_id payload
// through the administrator idempotency helper. The callback delegates to
// Operations.Update; accepted lease and work-context handling belong there, and
// helper errors determine the HTTP response.
// POST /api/v1/admin/system/update
func (h *SystemHandler) PerformUpdate(c *gin.Context) {
	operationID := buildSystemOperationID(c, "update")
	payload := gin.H{"operation_id": operationID}
	// Submit the identity as the idempotency payload before lease acquisition;
	// Operations owns accepted-work cancellation and completes its own lease
	// handling.
	h.ExecuteAdminIdempotentJSON(c, "admin.system.update", payload, h.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.operations.Update(ctx, operationID)
	})
}

// GetRollbackVersions returns the update service's allowed candidate entries
// inside a versions payload. Query failure becomes an internal-server response
// using its error text; a successful nil slice is passed through as supplied.
// GET /api/v1/admin/system/rollback-versions
func (h *SystemHandler) GetRollbackVersions(c *gin.Context) {
	versions, err := h.updateSvc.ListRollbackVersions(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, gin.H{
		"versions": versions,
	})
}

// Rollback submits local-backup restoration or a selected target through
// administrator idempotency and Operations. A nil body or zero ContentLength
// skips binding; unknown length still binds, and EOF is accepted as an empty
// request.
//
// Target text is trimmed. A nonempty target contributes to the operation
// identity as well as the version payload, so different targets produce
// different identity inputs before service validation. Invalid JSON other than
// EOF returns bad request. The handler performs no semantic version check
// itself; downloaded eligibility and the local installer outcome belong to the
// maintained update service.
// POST /api/v1/admin/system/rollback
func (h *SystemHandler) Rollback(c *gin.Context) {
	var req SystemRollbackRequest
	// Use body presence and length before binding, accepting EOF as omitted target
	// while unknown chunked length still reaches decoding.
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			response.Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	targetVersion := strings.TrimSpace(req.Version)

	operation := "rollback"
	if targetVersion != "" {
		operation = "rollback:" + targetVersion
	}
	operationID := buildSystemOperationID(c, operation)
	payload := gin.H{"operation_id": operationID, "version": targetVersion}
	// Keep the trimmed target in the command payload and maintenance identity,
	// then let Operations choose local or downloadable rollback under its lease.
	h.ExecuteAdminIdempotentJSON(c, "admin.system.rollback", payload, h.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.operations.Rollback(ctx, operationID, targetVersion)
	})
}

// RestartService builds a restart operation identity and submits its
// operation_id payload through administrator idempotency. Operations.Restart
// owns lease acquisition and the optional restart request; a successful
// response does not itself prove process exit.
// POST /api/v1/admin/system/restart
func (h *SystemHandler) RestartService(c *gin.Context) {
	operationID := buildSystemOperationID(c, "restart")
	payload := gin.H{"operation_id": operationID}
	// Run the restart callback through the HTTP command helper before Operations
	// acquires its lease and attempts the optional application request.
	h.ExecuteAdminIdempotentJSON(c, "admin.system.restart", payload, h.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.operations.Restart(ctx, operationID)
	})
}

// buildSystemOperationID derives the maintenance identity from a usable Gin
// context and operation text. An empty trimmed Idempotency-Key uses sysop,
// operation, and base36 Unix nanoseconds; that path performs no collision
// check.
//
// A supplied key hashes operation, administrator label, registered FullPath,
// and key, using admin:0 when no subject is present. Selected rollback callers
// include target text in operation. The hexadecimal SHA-256 digest is truncated
// to twenty-four characters before the prefix is added. Separators and
// shortening remain part of this representation; the helper supplies a stable
// input-derived label rather than an injective identity encoding.
func buildSystemOperationID(c *gin.Context, operation string) string {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		// Use the current wall-clock nanosecond value when no key is supplied;
		// repeated calls have no uniqueness barrier beyond that timestamp.
		return "sysop-" + operation + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	seed := operation + "|" + actorScope + "|" + c.FullPath() + "|" + key
	hash := idempotency.HashIdempotencyKey(seed)
	// Limit the visible digest to twenty-four hexadecimal characters, retaining a
	// shortened deterministic label rather than a full digest or collision check.
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "sysop-" + hash
}
