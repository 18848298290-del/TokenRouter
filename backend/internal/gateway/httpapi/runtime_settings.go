package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewaydto "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// GatewaySettingsEndpoints declares HTTP operations for the gateway settings service.
type GatewaySettingsEndpoints interface {
	GetRectifierSettings(*gin.Context)
	UpdateRectifierSettings(*gin.Context)
	GetBetaPolicySettings(*gin.Context)
	UpdateBetaPolicySettings(*gin.Context)
}

// RuntimeSettingsHandler reads and updates gateway policy settings.
type RuntimeSettingsHandler struct{ settingService *gateway.RuntimeSettings }

// UpdateRectifierSettingsRequest contains request-correction switches and signature patterns.
type UpdateRectifierSettingsRequest struct {
	Enabled                  bool     `json:"enabled"`
	ThinkingSignatureEnabled bool     `json:"thinking_signature_enabled"`
	ThinkingBudgetEnabled    bool     `json:"thinking_budget_enabled"`
	APIKeySignatureEnabled   bool     `json:"apikey_signature_enabled"`
	APIKeySignaturePatterns  []string `json:"apikey_signature_patterns"`
}

// UpdateBetaPolicySettingsRequest contains the beta policy rules to save.
type UpdateBetaPolicySettingsRequest struct {
	Rules []gatewaydto.BetaPolicyRule `json:"rules"`
}

// RegisterGatewaySettingsRoutes requires a settings group with authentication and auditing installed.
func RegisterGatewaySettingsRoutes(adminSettings *gin.RouterGroup, endpoint GatewaySettingsEndpoints) {
	adminSettings.GET("/rectifier", endpoint.GetRectifierSettings)
	adminSettings.PUT("/rectifier", endpoint.UpdateRectifierSettings)
	adminSettings.GET("/beta-policy", endpoint.GetBetaPolicySettings)
	adminSettings.PUT("/beta-policy", endpoint.UpdateBetaPolicySettings)
}

// NewRuntimeSettingsHandler binds the shared runtime settings service.
func NewRuntimeSettingsHandler(settings *gateway.RuntimeSettings) *RuntimeSettingsHandler {
	return &RuntimeSettingsHandler{settingService: settings}
}

// GetRectifierSettings returns request-correction settings.
func (h *RuntimeSettingsHandler) GetRectifierSettings(c *gin.Context) {
	settings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	patterns := settings.APIKeySignaturePatterns
	if patterns == nil {
		patterns = []string{}
	}
	httpx.Success(c, gatewaydto.RectifierSettings{
		Enabled:                  settings.Enabled,
		ThinkingSignatureEnabled: settings.ThinkingSignatureEnabled,
		ThinkingBudgetEnabled:    settings.ThinkingBudgetEnabled,
		APIKeySignatureEnabled:   settings.APIKeySignatureEnabled,
		APIKeySignaturePatterns:  patterns,
	})
}

// UpdateRectifierSettings updates request-correction settings.
func (h *RuntimeSettingsHandler) UpdateRectifierSettings(c *gin.Context) {
	var req UpdateRectifierSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Bound the pattern count and length before trimming and saving.
	const maxPatterns = 50
	const maxPatternLen = 500
	if len(req.APIKeySignaturePatterns) > maxPatterns {
		httpx.BadRequest(c, "Too many signature patterns (max 50)")
		return
	}
	var cleanedPatterns []string
	for _, p := range req.APIKeySignaturePatterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxPatternLen {
			httpx.BadRequest(c, "Signature pattern too long (max 500 characters)")
			return
		}
		cleanedPatterns = append(cleanedPatterns, p)
	}

	settings := &gateway.RectifierSettings{
		Enabled:                  req.Enabled,
		ThinkingSignatureEnabled: req.ThinkingSignatureEnabled,
		ThinkingBudgetEnabled:    req.ThinkingBudgetEnabled,
		APIKeySignatureEnabled:   req.APIKeySignatureEnabled,
		APIKeySignaturePatterns:  cleanedPatterns,
	}

	if err := h.settingService.SetRectifierSettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	updatedPatterns := updatedSettings.APIKeySignaturePatterns
	if updatedPatterns == nil {
		updatedPatterns = []string{}
	}
	httpx.Success(c, gatewaydto.RectifierSettings{
		Enabled:                  updatedSettings.Enabled,
		ThinkingSignatureEnabled: updatedSettings.ThinkingSignatureEnabled,
		ThinkingBudgetEnabled:    updatedSettings.ThinkingBudgetEnabled,
		APIKeySignatureEnabled:   updatedSettings.APIKeySignatureEnabled,
		APIKeySignaturePatterns:  updatedPatterns,
	})
}

// GetBetaPolicySettings returns beta policy settings.
func (h *RuntimeSettingsHandler) GetBetaPolicySettings(c *gin.Context) {
	settings, err := h.settingService.GetBetaPolicySettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	rules := make([]gatewaydto.BetaPolicyRule, len(settings.Rules))
	for i, r := range settings.Rules {
		rules[i] = gatewaydto.BetaPolicyRule(r)
	}
	httpx.Success(c, gatewaydto.BetaPolicySettings{Rules: rules})
}

// UpdateBetaPolicySettings updates beta policy settings.
func (h *RuntimeSettingsHandler) UpdateBetaPolicySettings(c *gin.Context) {
	var req UpdateBetaPolicySettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	rules := make([]gateway.BetaPolicyRule, len(req.Rules))
	for i, r := range req.Rules {
		rules[i] = gateway.BetaPolicyRule(r)
	}

	settings := &gateway.BetaPolicySettings{Rules: rules}
	if err := h.settingService.SetBetaPolicySettings(c.Request.Context(), settings); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}

	updated, err := h.settingService.GetBetaPolicySettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	outRules := make([]gatewaydto.BetaPolicyRule, len(updated.Rules))
	for i, r := range updated.Rules {
		outRules[i] = gatewaydto.BetaPolicyRule(r)
	}
	httpx.Success(c, gatewaydto.BetaPolicySettings{Rules: outRules})
}
