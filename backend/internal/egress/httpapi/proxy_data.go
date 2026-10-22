package httpapi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// proxy_data.go handles administrator proxy backup import and export requests.
// Proxy backup export supports selected IDs or filtered ordering and includes
// connection credentials in its administrator payload. Import validates the
// shared transfer envelope before delegating partial item processing, so
// endpoint responses can report reused, created, and rejected proxies from one
// submitted batch.

// ExportData returns an administrator proxy backup envelope containing
// credentials and an export timestamp. Selected positive IDs bypass filters;
// otherwise search is trimmed and capped at one hundred bytes before
// protocol/status/order values reach transfer export. The route owns step-up
// authentication.
func (h *ProxyHandler) ExportData(c *gin.Context) {
	ids, err := parseProxyIDs(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	search := strings.TrimSpace(c.Query("search"))
	// Match the listing endpoint's byte cap for stored-name search. Slicing
	// may split UTF-8, and the repository receives that same bounded search text.
	if len(search) > 100 {
		search = search[:100]
	}
	proxies, exported, err := h.transfer.Export(c.Request.Context(), egress.ProxyExportQuery{IDs: ids, Protocol: c.Query("protocol"), Status: c.Query("status"), Search: search, SortBy: c.DefaultQuery("sort_by", "id"), SortOrder: c.DefaultQuery("sort_order", "desc")})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, transfer.DataPayload{Type: transfer.DataType, Version: transfer.DataVersion, ExportedAt: exported, Proxies: proxies, Providers: []transfer.DataProvider{}})
}

// ImportData binds and validates a proxy transfer envelope before processing
// its proxy collection. Invalid JSON or envelope headers return bad request;
// successful batch processing returns partial creation/reuse/failure counts and
// diagnostics while repository-load failure uses the shared error response.
func (h *ProxyHandler) ImportData(c *gin.Context) {
	var req ProxyDataImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := transfer.ValidateHeader(req.Data); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.transfer.Import(c.Request.Context(), req.Data.Proxies)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, transfer.DataImportResult{ProxyCreated: result.ProxyCreated, ProxyReused: result.ProxyReused, ProxyFailed: result.ProxyFailed, Errors: result.Errors})
}

// parseProxyIDs parses repeated ids query values and comma-separated parts into
// positive integer IDs. Empty input returns nil, blank parts are ignored, and
// repeated IDs retain input order; any invalid or nonpositive part rejects the
// entire selection.
func parseProxyIDs(c *gin.Context) ([]int64, error) {
	values := c.QueryArray("ids")
	if len(values) == 0 {
		raw := strings.TrimSpace(c.Query("ids"))
		if raw != "" {
			values = []string{raw}
		}
	}
	if len(values) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(values))
	for _, item := range values {
		for part := range strings.SplitSeq(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("invalid proxy id: %s", part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}
