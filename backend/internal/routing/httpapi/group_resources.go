package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	groupdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// group_resources.go exposes group capacity, usage, linked keys, and per-user
// billing overrides through HTTP handlers.
// Group administration reads current capacity and linked API keys from their
// owning services, while usage summaries use the injected calendar's start of
// today. Live attestation availability is reported as capability data, and
// per-user rate or RPM changes delegate to billing administration.

// BatchSetGroupRateMultipliersRequest carries user-specific billing
// multipliers for one group.
type BatchSetGroupRateMultipliersRequest struct {
	Entries []billing.GroupRateMultiplierInput `json:"entries" binding:"required"` // submitted user multiplier entries
}

// BatchSetGroupRPMOverridesRequest carries user-specific request-rate
// overrides for one group.
type BatchSetGroupRPMOverridesRequest struct {
	Entries []billing.GroupRPMOverrideInput `json:"entries" binding:"required"` // submitted user RPM entries
}

// GroupUsageSummary reports calendar-day and retained-total charges for an
// administrator group row.
type GroupUsageSummary struct {
	GroupID int64 `json:"group_id"` // stored group identifier
	// TodayCost, YesterdayCost, and TotalCost are user-funded USD amounts in
	// DashboardService's group summary. The PostgreSQL reader derives them from
	// usage actual_cost, using calendar cutoffs for day totals.
	TodayCost     float64 `json:"today_cost"`     // today retained usage charges
	YesterdayCost float64 `json:"yesterday_cost"` // yesterday retained usage charges
	TotalCost     float64 `json:"total_cost"`     // all retained usage charges
}

// GroupRateAdministration supplies user-specific group price and request-rate
// override operations.
type GroupRateAdministration interface {
	// GetGroupRateMultipliers returns user-specific billing factors for the
	// requested group. A nil result represents no entries; the HTTP handler
	// turns it into an empty JSON array for clients.
	GetGroupRateMultipliers(context.Context, int64) ([]billing.UserGroupRateEntry, error)
	// ClearGroupRateMultipliers removes all stored user-specific price factors
	// for the requested group.
	ClearGroupRateMultipliers(context.Context, int64) error
	// BatchSetGroupRateMultipliers saves the supplied users' billing factors for
	// the requested group.
	BatchSetGroupRateMultipliers(context.Context, int64, []billing.GroupRateMultiplierInput) error
	// BatchSetGroupRPMOverrides saves the supplied users' request-rate overrides
	// for the requested group.
	BatchSetGroupRPMOverrides(context.Context, int64, []billing.GroupRPMOverrideInput) error
	// ClearGroupRPMOverrides removes all stored user-specific RPM overrides for
	// the requested group.
	ClearGroupRPMOverrides(context.Context, int64) error
}

// GroupResources binds observation readers and linked-resource administration
// for group HTTP routes. Supply each dependency used by registered handlers
// before serving requests; the handlers invoke those dependencies directly.
type GroupResources struct {
	Capacity interface { // current group capacity reader
		// GetAllGroupCapacity returns aggregated concurrency, session, and RPM
		// observations for active groups. Query failures propagate to the handler,
		// which returns its generic capacity-summary error response.
		GetAllGroupCapacity(context.Context) ([]routing.GroupCapacitySummary, error)
	}
	Keys  func(context.Context, int64, int, int) ([]keydto.APIKey[groupdto.Group], int64, error) // paginated linked API-key reader
	Rates GroupRateAdministration                                                                // per-user group override administrator
	// UsageSummary and Today use the same calendar interpretation. Today
	// supplies the start of the local day, and UsageSummary aggregates today,
	// yesterday, and retained totals using that instant.
	UsageSummary func(context.Context, time.Time) ([]GroupUsageSummary, error) // calendar-based group usage reader
	Today        func() time.Time                                              // server-calendar start-of-today clock
	// LiveCapability returns nil when attestation is available; a returned error
	// becomes supported=false and its text becomes the response reason.
	LiveCapability func(context.Context) error // Live attestation readiness checker
}

// GetLiveCapability returns the server's current Live attestation readiness
// as capability data. A checker error still produces HTTP success with
// supported=false and its error text as reason; the successful check produces
// supported=true.
func (h *GroupHandler) GetLiveCapability(c *gin.Context) {
	err := h.resources.LiveCapability(c.Request.Context())
	result := gin.H{"supported": err == nil}
	// A readiness failure is the capability query's answer, so include its
	// reason in a successful response that clients can display.
	if err != nil {
		result["reason"] = err.Error()
	}
	response.Success(c, result)
}

// GetUsageSummary returns today, yesterday, and retained-total group charges
// using the injected calendar. It passes Today's start instant to the usage
// reader and turns a read failure into the generic HTTP 500 summary response.
func (h *GroupHandler) GetUsageSummary(c *gin.Context) {
	todayStart := h.resources.Today()

	results, err := h.resources.UsageSummary(c.Request.Context(), todayStart)
	if err != nil {
		response.Error(c, 500, "Failed to get group usage summary")
		return
	}

	response.Success(c, results)
}

// GetCapacitySummary returns aggregate concurrency, session, and RPM
// observations for active groups. It delegates the request context to the
// capacity reader and returns the generic HTTP 500 summary response when that
// query fails.
func (h *GroupHandler) GetCapacitySummary(c *gin.Context) {
	results, err := h.resources.Capacity.GetAllGroupCapacity(c.Request.Context())
	if err != nil {
		response.Error(c, 500, "Failed to get group capacity summary")
		return
	}
	response.Success(c, results)
}

// GetGroupAPIKeys returns a paginated list of keys linked to the route's
// group ID. It parses the numeric ID and standard page parameters, then
// returns the reader's total or its structured error response.
func (h *GroupHandler) GetGroupAPIKeys(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	page, pageSize := response.ParsePagination(c)

	keys, total, err := h.resources.Keys(c.Request.Context(), groupID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Paginated(c, keys, total, page, pageSize)
}

// GetGroupRateMultipliers returns user-specific billing factors for the
// route's group ID. A malformed ID returns a bad request, reader errors use
// the standard error response, and a nil entry list is encoded as an empty
// array.
func (h *GroupHandler) GetGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	entries, err := h.resources.Rates.GetGroupRateMultipliers(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Clients consume a list of user overrides; encode an empty array when the
	// reader has no entries so JSON does not expose null.
	if entries == nil {
		entries = []billing.UserGroupRateEntry{}
	}
	response.Success(c, entries)
}

// ClearGroupRateMultipliers removes every stored user-specific billing factor
// for the route's group ID. It delegates the request context after numeric ID
// parsing and returns success when administration completes.
func (h *GroupHandler) ClearGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	if err := h.resources.Rates.ClearGroupRateMultipliers(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rate multipliers cleared successfully"})
}

// BatchSetGroupRateMultipliers decodes and saves user-specific billing
// factors for the route's group ID. Numeric ID and JSON binding failures
// return bad requests; administration validates and applies the supplied
// entries before success is reported.
func (h *GroupHandler) BatchSetGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req BatchSetGroupRateMultipliersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.resources.Rates.BatchSetGroupRateMultipliers(c.Request.Context(), groupID, req.Entries); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rate multipliers updated successfully"})
}

// BatchSetGroupRPMOverrides decodes and saves user-specific RPM overrides for
// the route's group ID. Numeric ID and JSON binding failures return bad
// requests; administration validates and applies the supplied entries before
// success is reported.
func (h *GroupHandler) BatchSetGroupRPMOverrides(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req BatchSetGroupRPMOverridesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.resources.Rates.BatchSetGroupRPMOverrides(c.Request.Context(), groupID, req.Entries); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "RPM overrides updated successfully"})
}

// ClearGroupRPMOverrides removes every stored user-specific RPM override for
// the route's group ID. It delegates the request context after numeric ID
// parsing and returns success when administration completes.
func (h *GroupHandler) ClearGroupRPMOverrides(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	if err := h.resources.Rates.ClearGroupRPMOverrides(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "RPM overrides cleared successfully"})
}
