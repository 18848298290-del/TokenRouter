package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// error_rules.go serves administrative HTTP operations for stored upstream
// error passthrough rules.
// Final upstream error rules are administered as complete stored values, but
// HTTP updates accept partial JSON fields. Creation supplies enabled and
// passthrough defaults, while updating starts from the loaded rule so omitted
// settings retain their current values. Condition lists are normalized to
// arrays before the service validates and saves them.
//
// The request representation treats null scalar pointers and null lists like
// omitted input. Empty arrays can clear condition lists, but null cannot clear
// response-code, custom-message or description pointers through this handler.
// Rule matching, priority and publication remain owned by the shared service;
// HTTP validates route and body syntax, maps validation errors and returns its
// saved result.

// ErrorPassthroughHandler serves HTTP requests for error passthrough rules.
type ErrorPassthroughHandler struct {
	service *errorpolicy.ErrorPassthroughService // shared persisted error-rule service
}

// CreateErrorPassthroughRuleRequest contains fields for creating an error rule.
type CreateErrorPassthroughRuleRequest struct {
	// required rule display name
	Name string `json:"name" binding:"required"`
	// Enabled defaults true when omitted or null during creation. A supplied false
	// value disables the new rule while retaining its configured conditions.
	Enabled    *bool `json:"enabled"`
	Priority   int   `json:"priority"`    // rule evaluation ordering number
	ErrorCodes []int `json:"error_codes"` // upstream status match candidates
	// case-insensitive message match candidates
	Keywords []string `json:"keywords"`
	// MatchMode defaults to any when the supplied string is empty. The service
	// validates whether a nonempty value names a supported condition combination.
	MatchMode string   `json:"match_mode"`
	Platforms []string `json:"platforms"` // eligible upstream platform names
	// PassthroughCode defaults true when omitted or null during creation. False
	// selects ResponseCode and requires a usable replacement status during service
	// validation.
	PassthroughCode *bool `json:"passthrough_code"`
	// ResponseCode supplies the replacement status when PassthroughCode is false.
	// Its pointer is passed to service validation, which requires a positive value
	// for that action.
	ResponseCode *int `json:"response_code"`
	// PassthroughBody defaults true when omitted or null during creation. False
	// selects CustomMessage and requires a nonempty replacement message during
	// service validation.
	PassthroughBody *bool `json:"passthrough_body"`
	// CustomMessage supplies client replacement text when PassthroughBody is
	// false. The service rejects a missing or empty message when that response
	// action is selected.
	CustomMessage *string `json:"custom_message"`
	// ordinary Ops monitoring suppression
	SkipMonitoring *bool `json:"skip_monitoring"`
	// optional administrator rule explanation
	Description *string `json:"description"`
}

// UpdateErrorPassthroughRuleRequest contains optional fields for a partial rule update.
type UpdateErrorPassthroughRuleRequest struct {
	// Name, Enabled, Priority, MatchMode, PassthroughCode, ResponseCode,
	// PassthroughBody, CustomMessage, SkipMonitoring and Description retain
	// existing values when their pointers are nil. Supplied pointers can replace
	// them with zero values. ErrorCodes, Keywords and Platforms retain existing
	// lists when nil; supplied empty arrays clear those lists.
	Name       *string  `json:"name"`        // supplied rule display name
	Enabled    *bool    `json:"enabled"`     // supplied rule enabled state
	Priority   *int     `json:"priority"`    // supplied rule ordering number
	ErrorCodes []int    `json:"error_codes"` // supplied upstream status candidates
	Keywords   []string `json:"keywords"`    // supplied message match candidates
	MatchMode  *string  `json:"match_mode"`  // supplied condition combination mode
	Platforms  []string `json:"platforms"`   // supplied upstream platform candidates
	// supplied upstream status passthrough
	PassthroughCode *bool `json:"passthrough_code"`
	// ResponseCode supplies a replacement status when the merged PassthroughCode
	// is false. The rule service validates that resulting action before persisting
	// this partial update.
	ResponseCode *int `json:"response_code"`
	// supplied upstream message passthrough
	PassthroughBody *bool `json:"passthrough_body"`
	// CustomMessage supplies client text when the merged PassthroughBody is false.
	// A supplied empty string can therefore fail resulting rule validation when
	// message passthrough is disabled.
	CustomMessage  *string `json:"custom_message"`
	SkipMonitoring *bool   `json:"skip_monitoring"` // supplied ordinary Ops suppression
	// supplied administrator rule explanation
	Description *string `json:"description"`
}

// NewErrorPassthroughHandler binds the shared error-rule service to HTTP
// handlers.
func NewErrorPassthroughHandler(service *errorpolicy.ErrorPassthroughService) *ErrorPassthroughHandler {
	return &ErrorPassthroughHandler{service: service}
}

// List returns the shared service's error rules in the admin success envelope.
// It uses the request context; service failures pass through the common error
// response helper for this authenticated route.
func (h *ErrorPassthroughHandler) List(c *gin.Context) {
	rules, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, rules)
}

// GetByID reads one rule using the decimal route ID. Invalid ID syntax receives
// 400, missing rules receive 404 and service errors use shared presentation;
// this handler does not apply rule matching to the lookup.
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

// Create binds JSON and constructs an error rule with enabled and passthrough
// switches defaulting true. Empty match mode selects any; optional response
// fields retain their pointers. Nil condition lists become arrays before
// validation and service persistence, and validation failures receive 400.
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

	// Normalize missing condition and platform slices before persistence so saved
	// rule responses expose arrays and rule matching sees empty configured lists.
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

// Update applies supplied JSON fields to the rule loaded by decimal ID and
// persists the resulting complete value. Missing IDs return 404 and invalid
// input returns 400 before saving.
//
// Scalar pointers distinguish supplied values from omitted or null fields,
// which retain existing values. Slice fields follow the same nil check, but an
// empty array replaces the old list. Optional response-code, custom-message and
// description pointers cannot be cleared by JSON null through this request
// representation. Starting from existing values keeps omitted settings and rule
// identity together before service validation; nil lists are normalized for
// response shape.
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

	// Begin with all loaded values before applying pointer and slice presence
	// checks, keeping partial HTTP input from resetting omitted rule actions or conditions.
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

	// Normalize missing condition and platform slices before persistence so saved
	// rule responses expose arrays and rule matching sees empty configured lists.
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

// Delete removes the rule identified by the decimal route ID. Invalid syntax
// returns 400, service failures use shared presentation and successful removal
// returns the standard admin success envelope with its completion message.
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

// RegisterErrorPassthroughRoutes binds rule list, lookup, create, update and
// removal below the admin group. Assembly installs authentication, rate limits
// and auditing on that group before registration so these handlers receive the
// admin request context.
func RegisterErrorPassthroughRoutes(admin *gin.RouterGroup, endpoint *ErrorPassthroughHandler) {
	rules := admin.Group("/error-passthrough-rules")
	{
		adminapi.Register(rules, ManagementOperations["error_passthrough_rules.list"], endpoint.List)
		adminapi.Register(rules, ManagementOperations["error_passthrough_rules.get"], endpoint.GetByID)
		adminapi.Register(rules, ManagementOperations["error_passthrough_rules.create"], endpoint.Create)
		adminapi.Register(rules, ManagementOperations["error_passthrough_rules.update"], endpoint.Update)
		adminapi.Register(rules, ManagementOperations["error_passthrough_rules.delete"], endpoint.Delete)
	}
}
