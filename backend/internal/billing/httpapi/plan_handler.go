package httpapi

import (
	"maps"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Sale plan offers and administrator records expose different representations
// of the same service plan. Offers carry resolved display-language metadata,
// feature arrays and copied group collections, while administrator responses
// retain versioned content for editing through planRecord. Request validation
// and persistence remain with billing.Plans, and purchase/order operations use
// payment handlers.

// PlanHandler serves sale-offer views and administrator plan mutations through
// billing.Plans.
type PlanHandler struct {
	plans *billing.Plans // plan validation and persistence service
}

// NewPlanHandler binds a plan service without loading offers or starting
// resources. The caller supplies a usable service and route middleware;
// request methods delegate sale, validation and persistence policy to that
// service.
func NewPlanHandler(plans *billing.Plans) *PlanHandler { return &PlanHandler{plans: plans} }

// GetPlans returns sale plans using a dedicated offer shape with feature
// arrays and localization resolution. Service errors use the shared response;
// group IDs/rate maps and feature arrays are copied, while comparison/quota
// pointers remain shared for read-only serialization.
// GET /api/v1/payment/plans
func (h *PlanHandler) GetPlans(c *gin.Context) {
	plans, err := h.plans.ListPlansForSale(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// planWithPlatform is the sale-offer response with resolved text and grant
	// templates.
	type planWithPlatform struct {
		Resolution  locale.Resolution `json:"localization_resolution"` // resolved display-language metadata
		ID          int64             `json:"id"`                      // local plan record identifier
		Name        string            `json:"name"`                    // selected plan display name
		Description string            `json:"description"`             // selected plan description text
		Price       float64           `json:"price"`                   // configured plan sale amount
		// OriginalPrice retains the plan's optional comparison amount pointer. It is
		// omitted when nil, and serialization readers keep that shared amount
		// unchanged while building the offer.
		OriginalPrice *float64 `json:"original_price,omitempty"` // optional comparison sale amount
		Currency      string   `json:"currency,omitempty"`       // configured sale currency code
		// ValidityDays and ValidityUnit retain the plan's duration template
		// together. The grant service interprets weeks/months during issuance; this
		// offer mapping passes the configured quantity/unit through.
		ValidityDays int    `json:"validity_days"` // configured validity quantity
		ValidityUnit string `json:"validity_unit"` // configured validity quantity unit
		// GroupIDs and GroupRateMultipliers describe covered groups and selected
		// grant rates. The offer mapping copies both collections, leaving the
		// service plan's original group data available.
		GroupIDs             []int64           `json:"group_ids"`              // applicable subscription group identifiers
		GroupRateMultipliers map[int64]float64 `json:"group_rate_multipliers"` // plan group rate overrides
		// DailyLimitUSD, WeeklyLimitUSD and MonthlyLimitUSD retain the plan's
		// optional issued-quota template pointers. Nil values are omitted in this
		// offer, and callers leave shared amounts unchanged during serialization.
		DailyLimitUSD   *float64 `json:"daily_limit_usd,omitempty"`   // USD
		WeeklyLimitUSD  *float64 `json:"weekly_limit_usd,omitempty"`  // USD
		MonthlyLimitUSD *float64 `json:"monthly_limit_usd,omitempty"` // USD
		Features        []string `json:"features"`                    // parsed plan feature entries
		ProductName     string   `json:"product_name"`                // selected payment product name
		ForSale         bool     `json:"for_sale"`                    // new purchase availability flag
		SortOrder       int      `json:"sort_order"`                  // plan offer sort order
	}
	result := make([]planWithPlatform, 0, len(plans))
	for _, p := range plans {
		result = append(result, planWithPlatform{
			Resolution:           p.Resolution,
			ID:                   p.ID,
			Name:                 p.Name,
			Description:          p.Description,
			Price:                p.Price,
			OriginalPrice:        p.OriginalPrice,
			Currency:             p.Currency,
			ValidityDays:         p.ValidityDays,
			ValidityUnit:         p.ValidityUnit,
			GroupIDs:             append([]int64(nil), p.GroupIDs...),
			GroupRateMultipliers: ClonePlanOfferRates(p.GroupRateMultipliers),
			DailyLimitUSD:        p.DailyLimitUSD,
			WeeklyLimitUSD:       p.WeeklyLimitUSD,
			MonthlyLimitUSD:      p.MonthlyLimitUSD,
			Features:             ParsePlanFeatures(p.Features),
			ProductName:          p.ProductName,
			ForSale:              p.ForSale,
			SortOrder:            p.SortOrder,
		})
	}
	response.Success(c, result)
}

// ListPlans returns all service plans mapped to administrator record
// responses. Service errors use the shared response; localization source data,
// optional amounts and group collection ownership follow planRecord's
// read-only mapping rules.
// GET /api/v1/admin/payment/plans
func (h *PlanHandler) ListPlans(c *gin.Context) {
	plans, err := h.plans.ListPlans(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]*PlanRecordResponse, len(plans))
	for i, p := range plans {
		out[i] = planRecord(p)
	}
	response.Success(c, out)
}

// CreatePlan binds a plan creation request and delegates its
// validation/persistence to the service. Invalid JSON yields bad request,
// service failures use the shared error response, and a successful plan is
// returned in the administrator record shape with created status.
// POST /api/v1/admin/payment/plans
func (h *PlanHandler) CreatePlan(c *gin.Context) {
	var req billing.CreatePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	plan, err := h.plans.CreatePlan(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, planRecord(plan))
}

// UpdatePlan parses the path ID and binds the replacement request before
// delegating the plan update. Parsing/JSON errors yield bad request; service
// failures use shared mapping, and success returns the administrator record
// view.
// PUT /api/v1/admin/payment/plans/:id
func (h *PlanHandler) UpdatePlan(c *gin.Context) {
	id, ok := parsePlanID(c, "id")
	if !ok {
		return
	}
	var req billing.UpdatePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	plan, err := h.plans.UpdatePlan(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, planRecord(plan))
}

// DeletePlan parses the path ID and delegates deletion to the plan service.
// Malformed numeric text yields bad request, service errors use shared
// mapping, and success returns the existing deleted message object.
// DELETE /api/v1/admin/payment/plans/:id
func (h *PlanHandler) DeletePlan(c *gin.Context) {
	id, ok := parsePlanID(c, "id")
	if !ok {
		return
	}
	if err := h.plans.DeletePlan(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// parsePlanID parses signed decimal path text and reports success. A parse
// error writes bad request and returns false; zero or negative parsed values
// are passed through for the service to interpret.
func parsePlanID(c *gin.Context, paramName string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(paramName), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid "+paramName)
		return 0, false
	}
	return id, true
}

// ParsePlanFeatures returns trimmed nonempty entries from newline-separated
// text. Empty or all-blank input returns an allocated empty slice, keeping
// offer JSON as an array; entries retain their accepted order and input text
// remains unchanged.
func ParsePlanFeatures(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var out []string
	for line := range strings.SplitSeq(raw, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	// All supplied lines were blank after trimming, so return an allocated empty
	// array for the offer's feature list representation.
	if out == nil {
		return []string{}
	}
	return out
}

// ClonePlanOfferRates copies scalar group rates into independent map storage.
// Nil and empty maps produce an allocated empty map for offer JSON; callers
// can change the returned keys/amounts without altering the input map.
func ClonePlanOfferRates(in map[int64]float64) map[int64]float64 {
	// An absent group-rate map still has an object representation in offer JSON,
	// so empty inputs allocate the response map.
	if len(in) == 0 {
		return map[int64]float64{}
	}
	out := make(map[int64]float64, len(in))
	maps.Copy(out, in)
	return out
}
