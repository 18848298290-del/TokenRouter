package httpapi

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ops_ingress_reject_handler.go validates ingress-rejection query dimensions
// and serves aggregate lists and ingestion health.
// Ingress-rejection queries address aggregate rows by minute, reason, route,
// protocol, and caller identities. Public enum filters reject unknown values
// before storage, while client addresses are normalized to the same
// representation used by ingress recording: mapped IPv4 becomes IPv4, and IPv6
// becomes its masked /64 address.
//
// The handler defaults its time range through the shared Ops parser and caps
// pagination at two hundred rows. Optional user and API-key filters require
// positive decimal IDs, so callers can narrow aggregates to recorded identities
// without treating a missing filter as zero. List and health queries pass
// through monitoring checks, and unavailable service state is reported before
// other parameters are parsed.

// These allowlists define the exact case-sensitive reason, route-family, and
// protocol names accepted by aggregate-list HTTP filters.
var (
	// ingressRejectReasons contains the rejection classifications selectable
	// through the reason query parameter.
	ingressRejectReasons = map[string]struct{}{
		"query_api_key_deprecated": {}, "api_key_required": {}, "invalid_api_key": {},
		"invalid_auth_rate_limited": {},
		"api_key_auth_overloaded":   {},
		"api_key_disabled":          {}, "ip_restricted": {}, "user_inactive": {}, "group_deleted": {},
		"group_disabled": {}, "group_not_allowed": {}, "group_unassigned": {}, "other": {},
	}

	// ingressRejectRouteFamilies contains route categories selectable
	// independently from the protocol filter.
	ingressRejectRouteFamilies = map[string]struct{}{
		"antigravity": {}, "gemini": {}, "codex": {}, "messages": {}, "responses": {},
		"chat_completions": {}, "images": {}, "videos": {}, "embeddings": {}, "models": {}, "other": {},
	}

	// ingressRejectProtocols contains observed protocol categories selectable
	// through the protocol query parameter.
	ingressRejectProtocols = map[string]struct{}{
		"google": {}, "anthropic": {}, "openai": {}, "gateway": {}, "other": {},
	}
)

// ingressRejectQueryError carries a filter-validation message returned to the
// administrator.
type ingressRejectQueryError struct {
	message string /* validation response message */
}

// ListIngressRejects validates aggregate filters and returns their paginated
// repository result. It reports an unavailable Ops service first, defaults the
// time range to one hour through parseOpsTimeRange, and limits page size to two
// hundred.
//
// Enum values are trimmed and matched case-sensitively. Client IP is parsed,
// unmapped, and reduced to /64 for IPv6 to match middleware aggregation; user
// and API-key filters accept positive decimal IDs. The service performs the
// monitoring check after parameter validation, and repository failures use the
// shared error response.
func (h *OpsHandler) ListIngressRejects(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	page, pageSize := response.ParsePagination(c)
	// Limit one aggregate-list response to two hundred stored rows.
	if pageSize > 200 {
		pageSize = 200
	}
	startTime, endTime, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	filter := &ops.OpsIngressRejectFilter{Page: page, PageSize: pageSize}
	if !startTime.IsZero() {
		filter.StartTime = &startTime
	}
	if !endTime.IsZero() {
		filter.EndTime = &endTime
	}
	if filter.RejectReason, err = parseIngressRejectEnum(c, "reason", ingressRejectReasons); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if filter.RouteFamily, err = parseIngressRejectEnum(c, "route_family", ingressRejectRouteFamilies); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if filter.Protocol, err = parseIngressRejectEnum(c, "protocol", ingressRejectProtocols); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if raw := strings.TrimSpace(c.Query("client_ip")); raw != "" {
		addr, parseErr := netip.ParseAddr(raw)
		if parseErr != nil {
			response.BadRequest(c, "Invalid client_ip")
			return
		}
		// Use the recording middleware's address representation so a full IPv6 input
		// matches its stored /64 aggregate and mapped IPv4 matches IPv4.
		addr = addr.Unmap()
		if addr.Is6() {
			addr = netip.PrefixFrom(addr, 64).Masked().Addr()
		}
		filter.ClientIP = addr.String()
	}
	if filter.UserID, err = parseOptionalPositiveID(c, "user_id"); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if filter.APIKeyID, err = parseOptionalPositiveID(c, "api_key_id"); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	result, err := h.opsService.ListIngressRejects(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// GetIngressRejectHealth returns aggregator capacity, backlog, and counters
// after checking monitoring is enabled. A missing Ops service returns service
// unavailable; an unbound aggregator returns the service's capacity-only health
// value.
func (h *OpsHandler) GetIngressRejectHealth(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.opsService.GetIngressRejectHealth())
}

// parseIngressRejectEnum trims a named query parameter and returns an allowed
// case-sensitive value. Blank input omits the filter; any other unlisted value
// returns a validation error identifying that parameter.
func parseIngressRejectEnum(c *gin.Context, name string, allowed map[string]struct{}) (string, error) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return "", nil
	}
	if _, ok := allowed[value]; !ok {
		return "", &ingressRejectQueryError{message: "Invalid " + name}
	}
	return value, nil
}

// parseOptionalPositiveID returns a pointer to a positive decimal int64 query
// value. Blank input omits the identity filter; malformed, nonpositive, or
// overflowing input returns a parameter-specific validation error.
func parseOptionalPositiveID(c *gin.Context, name string) (*int64, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return nil, &ingressRejectQueryError{message: "Invalid " + name}
	}
	return &value, nil
}

// Error returns the query-validation message for the HTTP response.
func (e *ingressRejectQueryError) Error() string { return e.message }
