package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// System-log administration lists indexed observations and submits filtered
// deletion requests after monitoring checks. Listing caps pages and validates
// supplied numeric identity filters, while cleanup additionally requires a positive
// context subject and validates API-key identity itself; user and provider
// cleanup IDs are passed to storage's positive-only filter rules. Deletion
// audit delivery belongs to the service's separate attempt.

// opsSystemLogCleanupRequest carries optional filters for administrator
// deletion of indexed system logs.
type opsSystemLogCleanupRequest struct {
	// StartTime and EndTime independently omit their bounds when blank and
	// otherwise use the two RFC3339 layouts. The cleanup service checks relative
	// ordering when both parsed pointers exist.
	StartTime string `json:"start_time"` // RFC3339
	EndTime   string `json:"end_time"`   // RFC3339
	Host      string `json:"host"`       // exact recorded host filter

	Level     string `json:"level"`     // case-folded stored level filter
	Component string `json:"component"` // exact stored component filter
	// RequestID is passed to PostgreSQL request lookup after trimming. Exact
	// stored local IDs suppress external alias matching in that lookup.
	RequestID       string `json:"request_id"`        // request lookup identifier filter
	ClientRequestID string `json:"client_request_id"` // exact client correlation filter
	// UserID, APIKeyID, and ProviderID identify optional stored records. Nil omits
	// a predicate, and PostgreSQL uses positive values; the HTTP handler
	// additionally rejects nonpositive APIKeyID.
	UserID *int64 `json:"user_id"` // optional recorded user identity
	// APIKeyID is omitted when nil. A supplied nonpositive value is rejected by
	// the HTTP cleanup handler before service deletion.
	APIKeyID   *int64 `json:"api_key_id"`  // optional recorded Key identity
	ProviderID *int64 `json:"provider_id"` // optional recorded provider identity
	Platform   string `json:"platform"`    // exact stored platform filter
	Model      string `json:"model"`       // exact stored model filter
	// Query becomes an ILIKE pattern across message, IDs, and extra JSON text.
	// PostgreSQL wraps it in percent signs and retains supplied wildcard
	// characters.
	Query string `json:"q"` // case-insensitive query pattern
}

// ListSystemLogs returns a paginated indexed-log response after monitoring
// checks. It parses a one-hour default range, caps size at two hundred, trims
// text filters, and requires positive supplied user, Key, and provider IDs.
// GET /api/v1/admin/ops/system-logs
func (h *OpsHandler) ListSystemLogs(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	page, pageSize := response.ParsePagination(c)
	// Limit indexed-log HTTP pages to the repository's two-hundred-row maximum.
	if pageSize > 200 {
		pageSize = 200
	}

	start, end, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	filter := &ops.OpsSystemLogFilter{
		Page:            page,
		PageSize:        pageSize,
		StartTime:       &start,
		EndTime:         &end,
		Host:            strings.TrimSpace(c.Query("host")),
		Level:           strings.TrimSpace(c.Query("level")),
		Component:       strings.TrimSpace(c.Query("component")),
		RequestID:       strings.TrimSpace(c.Query("request_id")),
		ClientRequestID: strings.TrimSpace(c.Query("client_request_id")),
		Platform:        strings.TrimSpace(c.Query("platform")),
		Model:           strings.TrimSpace(c.Query("model")),
		Query:           strings.TrimSpace(c.Query("q")),
	}
	if v := strings.TrimSpace(c.Query("user_id")); v != "" {
		id, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr != nil || id <= 0 {
			response.BadRequest(c, "Invalid user_id")
			return
		}
		filter.UserID = &id
	}
	if v := strings.TrimSpace(c.Query("api_key_id")); v != "" {
		id, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr != nil || id <= 0 {
			response.BadRequest(c, "Invalid api_key_id")
			return
		}
		filter.APIKeyID = &id
	}
	if v := strings.TrimSpace(c.Query("provider_id")); v != "" {
		id, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr != nil || id <= 0 {
			response.BadRequest(c, "Invalid provider_id")
			return
		}
		filter.ProviderID = &id
	}

	result, err := h.opsService.ListSystemLogs(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, result.Logs, int64(result.Total), result.Page, result.PageSize)
}

// CleanupSystemLogs binds deletion filters after monitoring and positive
// context-subject checks, then returns the affected-row count. Blank time
// strings omit bounds, invalid RFC3339 text or nonpositive Key IDs fail
// locally, and service/storage own effective filtering and separate audit
// attempts.
// POST /api/v1/admin/ops/system-logs/cleanup
func (h *OpsHandler) CleanupSystemLogs(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req opsSystemLogCleanupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	parseTS := func(raw string) (*time.Time, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, nil
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return &t, nil
		}
		// Try the second timestamp layout before returning a parse error, keeping
		// cleanup input consistent with other two-layout Ops parsers.
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}
	start, err := parseTS(req.StartTime)
	if err != nil {
		response.BadRequest(c, "Invalid start_time")
		return
	}
	end, err := parseTS(req.EndTime)
	if err != nil {
		response.BadRequest(c, "Invalid end_time")
		return
	}
	if req.APIKeyID != nil && *req.APIKeyID <= 0 {
		response.BadRequest(c, "Invalid api_key_id")
		return
	}

	filter := &ops.OpsSystemLogCleanupFilter{
		StartTime:       start,
		EndTime:         end,
		Host:            strings.TrimSpace(req.Host),
		Level:           strings.TrimSpace(req.Level),
		Component:       strings.TrimSpace(req.Component),
		RequestID:       strings.TrimSpace(req.RequestID),
		ClientRequestID: strings.TrimSpace(req.ClientRequestID),
		UserID:          req.UserID,
		APIKeyID:        req.APIKeyID,
		ProviderID:      req.ProviderID,
		Platform:        strings.TrimSpace(req.Platform),
		Model:           strings.TrimSpace(req.Model),
		Query:           strings.TrimSpace(req.Query),
	}

	deleted, err := h.opsService.CleanupSystemLogs(c.Request.Context(), filter, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": deleted})
}

// GetSystemLogIngestionHealth returns the bound sink's health after checking
// monitoring. Missing Ops service state is unavailable; an unbound sink returns
// zero-valued health through the service accessor.
// GET /api/v1/admin/ops/system-logs/health
func (h *OpsHandler) GetSystemLogIngestionHealth(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.opsService.GetSystemLogSinkHealth())
}
