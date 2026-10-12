package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ErrorPassthroughHandler serves HTTP requests for error passthrough rules.
type ErrorPassthroughHandler struct {
	service *errorpolicy.ErrorPassthroughService
}

// CreateErrorPassthroughRuleRequest contains fields for creating an error rule.
type CreateErrorPassthroughRuleRequest struct {
	Name            string   `json:"name" binding:"required"`
	Enabled         *bool    `json:"enabled"`
	Priority        int      `json:"priority"`
	ErrorCodes      []int    `json:"error_codes"`
	Keywords        []string `json:"keywords"`
	MatchMode       string   `json:"match_mode"`
	Platforms       []string `json:"platforms"`
	PassthroughCode *bool    `json:"passthrough_code"`
	ResponseCode    *int     `json:"response_code"`
	PassthroughBody *bool    `json:"passthrough_body"`
	CustomMessage   *string  `json:"custom_message"`
	SkipMonitoring  *bool    `json:"skip_monitoring"`
	Description     *string  `json:"description"`
}

// UpdateErrorPassthroughRuleRequest contains optional fields for a partial rule update.
type UpdateErrorPassthroughRuleRequest struct {
	Name            *string  `json:"name"`
	Enabled         *bool    `json:"enabled"`
	Priority        *int     `json:"priority"`
	ErrorCodes      []int    `json:"error_codes"`
	Keywords        []string `json:"keywords"`
	MatchMode       *string  `json:"match_mode"`
	Platforms       []string `json:"platforms"`
	PassthroughCode *bool    `json:"passthrough_code"`
	ResponseCode    *int     `json:"response_code"`
	PassthroughBody *bool    `json:"passthrough_body"`
	CustomMessage   *string  `json:"custom_message"`
	SkipMonitoring  *bool    `json:"skip_monitoring"`
	Description     *string  `json:"description"`
}

// NewErrorPassthroughHandler creates an HTTP handler for error rules.
func NewErrorPassthroughHandler(service *errorpolicy.ErrorPassthroughService) *ErrorPassthroughHandler {
	return &ErrorPassthroughHandler{service: service}
}

// List returns all error rules.
// GET /api/v1/admin/error-passthrough-rules
func (h *ErrorPassthroughHandler) List(c *gin.Context) {
	rules, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, rules)
}

// GetByID returns an error rule by ID.
// GET /api/v1/admin/error-passthrough-rules/:id
func (h *ErrorPassthroughHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	rule, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if rule == nil {
		response.NotFound(c, "Rule not found")
		return
	}

	response.Success(c, rule)
}

// Create creates an error rule.
// POST /api/v1/admin/error-passthrough-rules
func (h *ErrorPassthroughHandler) Create(c *gin.Context) {
	var req CreateErrorPassthroughRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	rule := &errorpolicy.ErrorPassthroughRule{
		Name:       req.Name,
		Priority:   req.Priority,
		ErrorCodes: req.ErrorCodes,
		Keywords:   req.Keywords,
		Platforms:  req.Platforms,
	}

	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	} else {
		rule.Enabled = true
	}
	if req.MatchMode != "" {
		rule.MatchMode = req.MatchMode
	} else {
		rule.MatchMode = errorpolicy.MatchModeAny
	}
	if req.PassthroughCode != nil {
		rule.PassthroughCode = *req.PassthroughCode
	} else {
		rule.PassthroughCode = true
	}
	if req.PassthroughBody != nil {
		rule.PassthroughBody = *req.PassthroughBody
	} else {
		rule.PassthroughBody = true
	}
	if req.SkipMonitoring != nil {
		rule.SkipMonitoring = *req.SkipMonitoring
	}
	rule.ResponseCode = req.ResponseCode
	rule.CustomMessage = req.CustomMessage
	rule.Description = req.Description

	// Encode empty lists as JSON arrays.
	if rule.ErrorCodes == nil {
		rule.ErrorCodes = []int{}
	}
	if rule.Keywords == nil {
		rule.Keywords = []string{}
	}
	if rule.Platforms == nil {
		rule.Platforms = []string{}
	}

	created, err := h.service.Create(c.Request.Context(), rule)
	if err != nil {
		if _, ok := err.(*errorpolicy.ValidationError); ok {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, created)
}

// Update applies the supplied fields to an existing rule.
// PUT /api/v1/admin/error-passthrough-rules/:id
func (h *ErrorPassthroughHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	var req UpdateErrorPassthroughRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	existing, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if existing == nil {
		response.NotFound(c, "Rule not found")
		return
	}

	// Initialize from the existing rule so omitted fields retain their values.
	rule := &errorpolicy.ErrorPassthroughRule{
		ID:              id,
		Name:            existing.Name,
		Enabled:         existing.Enabled,
		Priority:        existing.Priority,
		ErrorCodes:      existing.ErrorCodes,
		Keywords:        existing.Keywords,
		MatchMode:       existing.MatchMode,
		Platforms:       existing.Platforms,
		PassthroughCode: existing.PassthroughCode,
		ResponseCode:    existing.ResponseCode,
		PassthroughBody: existing.PassthroughBody,
		CustomMessage:   existing.CustomMessage,
		SkipMonitoring:  existing.SkipMonitoring,
		Description:     existing.Description,
	}

	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.Priority != nil {
		rule.Priority = *req.Priority
	}
	if req.ErrorCodes != nil {
		rule.ErrorCodes = req.ErrorCodes
	}
	if req.Keywords != nil {
		rule.Keywords = req.Keywords
	}
	if req.MatchMode != nil {
		rule.MatchMode = *req.MatchMode
	}
	if req.Platforms != nil {
		rule.Platforms = req.Platforms
	}
	if req.PassthroughCode != nil {
		rule.PassthroughCode = *req.PassthroughCode
	}
	if req.ResponseCode != nil {
		rule.ResponseCode = req.ResponseCode
	}
	if req.PassthroughBody != nil {
		rule.PassthroughBody = *req.PassthroughBody
	}
	if req.CustomMessage != nil {
		rule.CustomMessage = req.CustomMessage
	}
	if req.Description != nil {
		rule.Description = req.Description
	}
	if req.SkipMonitoring != nil {
		rule.SkipMonitoring = *req.SkipMonitoring
	}

	// Encode empty lists as JSON arrays.
	if rule.ErrorCodes == nil {
		rule.ErrorCodes = []int{}
	}
	if rule.Keywords == nil {
		rule.Keywords = []string{}
	}
	if rule.Platforms == nil {
		rule.Platforms = []string{}
	}

	updated, err := h.service.Update(c.Request.Context(), rule)
	if err != nil {
		if _, ok := err.(*errorpolicy.ValidationError); ok {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, updated)
}

// Delete deletes an error rule.
// DELETE /api/v1/admin/error-passthrough-rules/:id
func (h *ErrorPassthroughHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid rule ID")
		return
	}

	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rule deleted successfully"})
}

// RegisterErrorPassthroughRoutes registers admin endpoints; app must install group authentication, rate limits, and
// auditing first.
func RegisterErrorPassthroughRoutes(admin *gin.RouterGroup, endpoint *ErrorPassthroughHandler) {
	rules := admin.Group("/error-passthrough-rules")
	{
		rules.GET("", endpoint.List)
		rules.GET("/:id", endpoint.GetByID)
		rules.POST("", endpoint.Create)
		rules.PUT("/:id", endpoint.Update)
		rules.DELETE("/:id", endpoint.Delete)
	}
}
