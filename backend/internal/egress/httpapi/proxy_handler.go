package httpapi

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	proxydto "github.com/TokenFlux/TokenRouter/internal/egress/httpapi/dto"
	idempotencyhttp "github.com/TokenFlux/TokenRouter/internal/idempotency/httpapi"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// proxy_handler.go serves proxy administration requests and translates request
// fields into service inputs.
// Proxy administration endpoints expose credential-bearing DTOs and interpret
// connection patches separately from expiration replacement. Incoming
// expiration uses positive Unix seconds, while nil or nonpositive values become
// absent time values before the administration service applies its write rules.
//
// Create delegates its bound request value through the common administrator
// idempotency executor before trimming connection text in the callback. Listing
// includes provider counts and optional probe cache values, backup handling
// delegates to ProxyTransfer, and batch creation skips per-item persistence
// failures while duplicate-query failures abort the remaining batch.
// Authentication is supplied by route wiring, and placeholder statistics remain
// fixed response values rather than measurements for the parsed proxy ID.

// ProxyHandler binds protected administrator proxy endpoints to persistence,
// probes, and backup transfer services.
type ProxyHandler struct {
	// Executor receives its coordinator through app binding before serving
	// requests. Its zero value executes callbacks directly for fixtures, and
	// binding cannot race request execution.
	idempotencyhttp.Executor // bound administrator command execution helper

	transfer     *egress.ProxyTransfer     // proxy backup import/export coordinator
	adminService egress.ProxyAdministrator // proxy administration operation dependency
}

// CreateProxyRequest contains validated administrator connection values and
// optional proxy expiration policy input.
//
// FallbackMode, BackupProxyID, ExpiresAt, and ExpiryWarnDays form the supplied
// expiration policy. Empty FallbackMode becomes none in administration, and
// proxy mode requires a backup identity.
type CreateProxyRequest struct {
	Name string `json:"name" binding:"required"` // administrator proxy display name
	// stored proxy scheme text
	Protocol string `json:"protocol" binding:"required,oneof=http https socks5 socks5h"`
	Host     string `json:"host" binding:"required"`                 // stored proxy hostname text
	Port     int    `json:"port" binding:"required,min=1,max=65535"` // proxy TCP port number
	Username string `json:"username"`                                // proxy authentication username text
	Password string `json:"password"`                                // submitted proxy authentication password
	// ExpiresAt supplies optional creation expiration in Unix seconds. The handler
	// constructs a UTC time only for positive values; nil and nonpositive values
	// select no expiration.
	ExpiresAt *int64 `json:"expires_at"` // Unix seconds
	// expiration provider reassignment mode
	FallbackMode   string `json:"fallback_mode" binding:"omitempty,oneof=none proxy direct"`
	BackupProxyID  *int64 `json:"backup_proxy_id"`                            // optional destination backup identity
	ExpiryWarnDays int    `json:"expiry_warn_days" binding:"omitempty,min=0"` // days
}

// UpdateProxyRequest carries connection-field patches and a complete
// expiration/fallback policy replacement.
//
// FallbackMode, BackupProxyID, ExpiresAt, and ExpiryWarnDays form the supplied
// expiration policy. Empty FallbackMode becomes none in administration, and
// proxy mode requires a backup identity. Name, Protocol, Host, Port, Username,
// Password, and Status update existing values only when their trimmed or
// numeric inputs remain nonzero.
type UpdateProxyRequest struct {
	Name string `json:"name"` // administrator proxy display name
	// stored proxy scheme text
	Protocol string `json:"protocol" binding:"omitempty,oneof=http https socks5 socks5h"`
	Host     string `json:"host"`                                     // stored proxy hostname text
	Port     int    `json:"port" binding:"omitempty,min=1,max=65535"` // proxy TCP port number
	Username string `json:"username"`                                 // proxy authentication username text
	Password string `json:"password"`                                 // submitted proxy authentication password
	// persisted proxy availability status
	Status string `json:"status" binding:"omitempty,oneof=active inactive"`
	// ExpiresAt is converted only when present and positive; nil or nonpositive
	// input becomes absent expiration. The update service treats that absent value
	// as replacement, not patch omission.
	ExpiresAt *int64 `json:"expires_at"` // Unix seconds
	// expiration provider reassignment mode
	FallbackMode   string `json:"fallback_mode" binding:"omitempty,oneof=none proxy direct"`
	BackupProxyID  *int64 `json:"backup_proxy_id"`                            // optional destination backup identity
	ExpiryWarnDays int    `json:"expiry_warn_days" binding:"omitempty,min=0"` // days
}

// BatchCreateProxyItem represents a single proxy in batch create request.
type BatchCreateProxyItem struct {
	// requested proxy scheme text
	Protocol string `json:"protocol" binding:"required,oneof=http https socks5 socks5h"`
	Host     string `json:"host" binding:"required"`                 // requested proxy hostname text
	Port     int    `json:"port" binding:"required,min=1,max=65535"` // requested TCP port number
	Username string `json:"username"`                                // requested proxy authentication username
	Password string `json:"password"`                                // requested proxy authentication password
}

// BatchCreateRequest holds the ordered nonempty connection list submitted for
// administrator batch creation.
type BatchCreateRequest struct {
	// ordered requested connection records
	Proxies []BatchCreateProxyItem `json:"proxies" binding:"required,min=1"`
}

// AdminProxy is the proxy response used by administrator endpoints.
type AdminProxy = proxydto.AdminProxy

// AdminProxyWithProviderCount includes proxy details and the number of bound providers.
type AdminProxyWithProviderCount = proxydto.AdminProxyWithProviderCount

// ProxyProviderSummary describes a provider bound to a proxy.
type ProxyProviderSummary = proxydto.ProxyProviderSummary

// NewProxyHandler constructs administration using the first optional transfer
// coordinator. Missing or nil first transfer selects a coordinator without
// background tasks; the application separately binds idempotency before serving
// production requests.
func NewProxyHandler(adminService egress.ProxyAdministrator, transfers ...*egress.ProxyTransfer) *ProxyHandler {
	var transfer *egress.ProxyTransfer
	if len(transfers) > 0 {
		transfer = transfers[0]
	}
	if transfer == nil {
		transfer = egress.NewProxyTransfer(adminService, nil, time.Now)
	}
	return &ProxyHandler{
		adminService: adminService, transfer: transfer,
	}
}

// List returns a paginated administrator proxy DTO collection with provider
// counts and cached probe details. It forwards protocol/status/order filters,
// trims search to a one-hundred-byte prefix, and uses common pagination parsing
// and error response helpers.
func (h *ProxyHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	protocol := c.Query("protocol")
	status := c.Query("status")
	search := c.Query("search")
	sortBy := c.DefaultQuery("sort_by", "id")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	// Match backup export's bounded search text before repository filtering.
	// The byte slice can split a UTF-8 sequence in an otherwise longer query.
	search = strings.TrimSpace(search)
	if len(search) > 100 {
		search = search[:100]
	}

	proxies, total, err := h.adminService.ListProxiesWithProviderCount(c.Request.Context(), page, pageSize, protocol, status, search, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]AdminProxyWithProviderCount, 0, len(proxies))
	for i := range proxies {
		out = append(out, *ProxyWithProviderCountFromServiceAdmin(&proxies[i]))
	}
	response.Paginated(c, out, total, page, pageSize)
}

// GetAll returns active-status administrator proxy responses without
// pagination. The exact with_count=true value chooses provider-count and probe
// augmentation; other values use ordinary proxy responses, and time-based
// expiration is evaluated separately by maintenance.
func (h *ProxyHandler) GetAll(c *gin.Context) {
	withCount := c.Query("with_count") == "true"

	if withCount {
		proxies, err := h.adminService.GetAllProxiesWithProviderCount(c.Request.Context())
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		out := make([]AdminProxyWithProviderCount, 0, len(proxies))
		for i := range proxies {
			out = append(out, *ProxyWithProviderCountFromServiceAdmin(&proxies[i]))
		}
		response.Success(c, out)
		return
	}

	proxies, err := h.adminService.GetAllProxies(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]AdminProxy, 0, len(proxies))
	for i := range proxies {
		out = append(out, *ProxyFromServiceAdmin(&proxies[i]))
	}
	response.Success(c, out)
}

// GetByID parses a signed decimal path ID and returns credential-bearing
// administrator proxy data. Parse failure is bad request; repository errors use
// shared response handling, with positivity and missing-record behavior
// determined by the delegated lookup.
func (h *ProxyHandler) GetByID(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	proxy, err := h.adminService.GetProxy(c.Request.Context(), proxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ProxyFromServiceAdmin(proxy))
}

// Create validates JSON and runs proxy creation through the bound administrator
// idempotency executor. The raw bound request is the command payload; the
// callback trims connection strings and converts positive Unix expiration
// seconds before service validation.
//
// The service returns a privileged proxy DTO containing its password, so
// routing supplies administrator access. Nil or nonpositive expiration becomes
// absent time and empty fallback mode is normalized by administration. A zero
// executor supports direct fixture callbacks, while production binding supplies
// persistence-backed replay decisions and their write retention period.
func (h *ProxyHandler) Create(c *gin.Context) {
	var req CreateProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	h.ExecuteAdminIdempotentJSON(c, "admin.proxies.create", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		var expiresAt *time.Time
		if req.ExpiresAt != nil && *req.ExpiresAt > 0 {
			t := time.Unix(*req.ExpiresAt, 0).UTC()
			expiresAt = &t
		}
		proxy, err := h.adminService.CreateProxy(ctx, &egress.CreateProxyInput{
			Name:           strings.TrimSpace(req.Name),
			Protocol:       strings.TrimSpace(req.Protocol),
			Host:           strings.TrimSpace(req.Host),
			Port:           req.Port,
			Username:       strings.TrimSpace(req.Username),
			Password:       strings.TrimSpace(req.Password),
			ExpiresAt:      expiresAt,
			FallbackMode:   strings.TrimSpace(req.FallbackMode),
			BackupProxyID:  req.BackupProxyID,
			ExpiryWarnDays: req.ExpiryWarnDays,
		})
		if err != nil {
			return nil, err
		}
		return ProxyFromServiceAdmin(proxy), nil
	})
}

// Update parses a decimal ID, validates JSON, and forwards connection patches
// plus complete expiration replacement to administration. Strings are trimmed,
// and only positive expiration seconds become time values; nil or nonpositive
// expiration clears the service's optional timestamp.
func (h *ProxyHandler) Update(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	var req UpdateProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt > 0 {
		t := time.Unix(*req.ExpiresAt, 0).UTC()
		expiresAt = &t
	}
	proxy, err := h.adminService.UpdateProxy(c.Request.Context(), proxyID, &egress.UpdateProxyInput{
		Name:           strings.TrimSpace(req.Name),
		Protocol:       strings.TrimSpace(req.Protocol),
		Host:           strings.TrimSpace(req.Host),
		Port:           req.Port,
		Username:       strings.TrimSpace(req.Username),
		Password:       strings.TrimSpace(req.Password),
		Status:         strings.TrimSpace(req.Status),
		ExpiresAt:      expiresAt,
		FallbackMode:   strings.TrimSpace(req.FallbackMode),
		BackupProxyID:  req.BackupProxyID,
		ExpiryWarnDays: req.ExpiryWarnDays,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ProxyFromServiceAdmin(proxy))
}

// Delete parses a signed decimal ID and delegates provider-count rejection and
// repository deletion. Parse failure returns bad request, service errors use
// common response handling, and success returns the fixed administrator
// confirmation payload.
func (h *ProxyHandler) Delete(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	err = h.adminService.DeleteProxy(c.Request.Context(), proxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Proxy deleted successfully"})
}

// BatchDelete validates a nonempty ID list and returns administration's
// independent per-item deletion results. It does not deduplicate IDs or
// validate each ID's positivity here; skipped errors remain part of the
// returned batch outcome.
func (h *ProxyHandler) BatchDelete(c *gin.Context) {
	var req BatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.adminService.BatchDeleteProxies(c.Request.Context(), req.IDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// Test parses a signed decimal ID and returns the service's connectivity result
// for its stored proxy. A failed probe can be a successful HTTP response
// carrying Success=false; lookup/service errors use shared response handling.
func (h *ProxyHandler) Test(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	result, err := h.adminService.TestProxy(c.Request.Context(), proxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// CheckQuality parses a signed decimal ID and returns a weighted proxy target
// assessment. Probe failures are represented by assessment items and counters;
// parsing or delegated service errors use the common administrator error
// response.
func (h *ProxyHandler) CheckQuality(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	result, err := h.adminService.CheckProxyQuality(c.Request.Context(), proxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// GetStats validates decimal path-ID syntax and returns the fixed placeholder
// statistics payload. It performs no repository lookup or measurements, so
// successful parsing yields the same values even for an absent proxy ID.
func (h *ProxyHandler) GetStats(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	_ = proxyID
	response.Success(c, gin.H{
		"total_providers":  0,
		"active_providers": 0,
		"total_requests":   0,
		"success_rate":     100.0,
		"average_latency":  0,
	})
}

// GetProxyProviders parses a signed decimal proxy ID and maps its
// bound-provider summaries for administrator display. Nullable Notes remains
// shared through mapping; parsing and service errors use the endpoint's common
// response helpers.
func (h *ProxyHandler) GetProxyProviders(c *gin.Context) {
	proxyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}

	providers, err := h.adminService.GetProxyProviders(c.Request.Context(), proxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]ProxyProviderSummary, 0, len(providers))
	for i := range providers {
		out = append(out, *ProxyProviderSummaryFromService(&providers[i]))
	}
	response.Success(c, out)
}

// BatchCreate validates an ordered connection list and creates each
// nonduplicate record with the fixed default name. Duplicate matching uses
// trimmed host, port, username, and password without protocol; a query error
// aborts remaining work.
//
// Each persistence failure increments skipped and allows later items to
// proceed, while earlier successful creations remain stored. The returned
// created/skipped counts therefore describe partial batch work, with duplicate
// and create failures combined in skipped. The administration service handles
// default expiration policy and optional new-record probes separately from this
// handler's iteration.
func (h *ProxyHandler) BatchCreate(c *gin.Context) {
	var req BatchCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	created := 0
	skipped := 0

	for _, item := range req.Proxies {
		host := strings.TrimSpace(item.Host)
		protocol := strings.TrimSpace(item.Protocol)
		username := strings.TrimSpace(item.Username)
		password := strings.TrimSpace(item.Password)

		// The repository's duplicate identity excludes protocol, so differently
		// named schemes with the same address/auth values count as an existing connection.
		exists, err := h.adminService.CheckProxyExists(c.Request.Context(), host, item.Port, username, password)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}

		if exists {
			skipped++
			continue
		}

		_, err = h.adminService.CreateProxy(c.Request.Context(), &egress.CreateProxyInput{
			Name:     "default",
			Protocol: protocol,
			Host:     host,
			Port:     item.Port,
			Username: username,
			Password: password,
		})
		if err != nil {
			// Keep earlier created records and continue later items after a
			// persistence failure. The response combines this failure with duplicate skips.
			skipped++
			continue
		}

		created++
	}

	response.Success(c, gin.H{
		"created": created,
		"skipped": skipped,
	})
}

// ProxyFromServiceAdmin maps a stored proxy into a password-bearing
// administrator DTO. Nil input returns nil, and source expiration/backup
// pointers remain shared through the underlying mapper; callers protect access
// before returning the response.
func ProxyFromServiceAdmin(p *egress.Proxy) *AdminProxy { return proxydto.ProxyFromEgressAdmin(p) }

// ProxyWithProviderCountFromServiceAdmin maps administrator connection data,
// provider counts, and optional cached probe values. Nil input returns nil, and
// mapped optional pointers remain shared with the source through response
// serialization.
func ProxyWithProviderCountFromServiceAdmin(p *egress.ProxyWithProviderCount) *AdminProxyWithProviderCount {
	return proxydto.ProxyWithProviderCountFromEgressAdmin(p)
}

// ProxyProviderSummaryFromService maps bound-provider summary data into
// administrator response values. Nil input returns nil, and optional Notes
// remains shared with the source until the response encoder consumes it.
func ProxyProviderSummaryFromService(a *egress.ProxyProviderSummary) *ProxyProviderSummary {
	return proxydto.ProxyProviderSummaryFromEgress(a)
}
