package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Ollama Cloud administration exposes managed browser-session state through
// dedicated usage endpoints. Provider selections require positive decimal IDs,
// session text remains an input to the core service, and auto-refresh uses a
// required boolean pointer so explicit false can be submitted.

// OllamaUsageHandler binds dedicated usage, managed-session and settings
// endpoints to Ollama maintenance.
type OllamaUsageHandler struct {
	ollamaCloudUsage *provider.OllamaCloudUsageService // borrowed managed Ollama usage
}

// ollamaCloudUsageSessionRequest carries required browser-session text for
// managed Ollama usage access.
type ollamaCloudUsageSessionRequest struct {
	Session string `json:"session" binding:"required"` // supplied browser session text
}

// ollamaCloudUsageAutoRefreshRequest distinguishes a supplied auto-refresh
// boolean from an omitted setting.
type ollamaCloudUsageAutoRefreshRequest struct {
	// Enabled requires pointer presence during binding so explicit false remains a
	// valid update. Omission or null is rejected before managed refresh policy
	// changes.
	Enabled *bool `json:"enabled" binding:"required"` // supplied auto-refresh policy boolean
}

// NewOllamaUsageHandler borrows the Ollama usage service for administrator
// session and settings requests. A nil service remains unavailable, and
// endpoint guards report the domain unavailable error before invoking core
// operations.
func NewOllamaUsageHandler(core *provider.OllamaCloudUsageService) *OllamaUsageHandler {
	return &OllamaUsageHandler{ollamaCloudUsage: core}
}

// GetOllamaCloudUsageSettings returns effective service settings using the
// request context. A nil service returns the domain unavailable response, and
// settings lookup failures retain their shared error mapping.
func (h *OllamaUsageHandler) GetOllamaCloudUsageSettings(c *gin.Context) {
	if h.ollamaCloudUsage == nil {
		response.ErrorFrom(c, provider.ErrOllamaCloudUsageUnavailable)
		return
	}
	settings, err := h.ollamaCloudUsage.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateOllamaCloudUsageSettings binds settings, requests validation and
// storage, then rereads the effective result. A failed reread returns an error
// after storage has completed; the nil-service guard precedes binding.
func (h *OllamaUsageHandler) UpdateOllamaCloudUsageSettings(c *gin.Context) {
	if h.ollamaCloudUsage == nil {
		response.ErrorFrom(c, provider.ErrOllamaCloudUsageUnavailable)
		return
	}
	var req provider.OllamaCloudUsageSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.ollamaCloudUsage.UpdateSettings(c.Request.Context(), &req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	settings, err := h.ollamaCloudUsage.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// GetOllamaCloudUsage returns the core service's display state for a positive
// provider ID. Service availability and ID validation run first; grouping,
// encryption flags and saved snapshot resolution belong to the use case.
func (h *OllamaUsageHandler) GetOllamaCloudUsage(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	state, err := h.ollamaCloudUsage.GetState(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

// SaveOllamaCloudUsageSession binds required session text for a positive
// provider ID and returns resulting display state. The core service validates
// and stores the managed session; input text is not sent back by this handler.
func (h *OllamaUsageHandler) SaveOllamaCloudUsageSession(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	var req ollamaCloudUsageSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	state, err := h.ollamaCloudUsage.SaveSession(c.Request.Context(), providerID, req.Session)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

// DeleteOllamaCloudUsageSession requests managed-session deletion for a
// positive provider ID and returns resulting display state. Availability and
// identifier checks precede the use case, whose errors retain the common
// mapping.
func (h *OllamaUsageHandler) DeleteOllamaCloudUsageSession(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	state, err := h.ollamaCloudUsage.DeleteSession(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

// SetOllamaCloudUsageAutoRefresh binds a required boolean pointer and applies
// its value for the selected provider. Explicit false is accepted, while
// omission is rejected before the core service can change managed refresh
// policy.
func (h *OllamaUsageHandler) SetOllamaCloudUsageAutoRefresh(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	var req ollamaCloudUsageAutoRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	state, err := h.ollamaCloudUsage.SetAutoRefresh(c.Request.Context(), providerID, *req.Enabled)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

// RefreshOllamaCloudUsage requests fresh usage state for a positive provider
// identifier. Availability and identifier checks precede maintenance, and the
// core service owns shared query lifetime, snapshot updates and its returned
// domain errors.
func (h *OllamaUsageHandler) RefreshOllamaCloudUsage(c *gin.Context) {
	if !h.requireOllamaCloudUsage(c) {
		return
	}
	providerID, ok := ollamaCloudUsageProviderID(c)
	if !ok {
		return
	}
	state, err := h.ollamaCloudUsage.Refresh(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

// requireOllamaCloudUsage reports whether a nonnil handler has its service
// dependency. Missing handler or service writes the unavailable domain response
// and returns false, allowing provider operations to stop before identifier
// binding.
func (h *OllamaUsageHandler) requireOllamaCloudUsage(c *gin.Context) bool {
	if h != nil && h.ollamaCloudUsage != nil {
		return true
	}
	response.ErrorFrom(c, provider.ErrOllamaCloudUsageUnavailable)
	return false
}

// ollamaCloudUsageProviderID parses a positive decimal provider ID and reports
// whether the handler can continue. Nil contexts return false silently; invalid
// or nonpositive IDs write 400 before any core operation.
func ollamaCloudUsageProviderID(c *gin.Context) (int64, bool) {
	if c == nil {
		return 0, false
	}
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || providerID <= 0 {
		response.BadRequest(c, "Invalid provider ID")
		return 0, false
	}
	return providerID, true
}
