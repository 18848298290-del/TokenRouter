package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Provider reports include complete calendar days in the timezone supplied by
// application assembly. The requested span includes today and ends at
// tomorrow's day start, so a days value describes calendar dates rather than a
// fixed number of elapsed hours.

// ProviderReportOptions supplies provider usage queries and calendar callbacks
// for date-range reports.
type ProviderReportOptions struct {
	// Now, StartOfDay and Query are required by GetStats and retained by the
	// handler. Now and StartOfDay share a calendar; Query receives the resulting
	// half-open date range under the HTTP request context.
	// calendar current-time callback
	Now func() time.Time
	// configured calendar day boundary
	StartOfDay func(time.Time) time.Time
	// provider usage range query
	Query func(context.Context, int64, time.Time, time.Time) (*usage.ProviderUsageStatsResponse, error)
}

// GetStats returns provider usage across the requested calendar-day span,
// including today. Malformed or out-of-range days selects 30; the injected
// calendar computes both boundaries before the report query receives the
// request context.
// GET /api/v1/admin/providers/:id/stats
func (h *ManagementHandler) GetStats(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	// Malformed and out-of-range values use the standard monthly display span,
	// keeping report queries between one and ninety calendar days.
	days := 30
	if daysStr := c.Query("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 && d <= 90 {
			days = d
		}
	}

	// Calendar boundaries include today and stop before tomorrow. The injected
	// calendar applies its timezone, including daylight-saving changes in day length.
	now := h.reports.Now()
	endTime := h.reports.StartOfDay(now.AddDate(0, 0, 1))
	startTime := h.reports.StartOfDay(now.AddDate(0, 0, -days+1))

	stats, err := h.reports.Query(c.Request.Context(), providerID, startTime, endTime)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, stats)
}
