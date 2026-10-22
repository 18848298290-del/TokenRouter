package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewaydto "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// runtime_settings.go exposes admin reads and replacements for request
// correction settings and beta policy rules.
// Gateway correction switches and beta rules are runtime settings stored
// through one shared service. Admin updates validate their supplied shape, save
// the complete configuration and read it back for response DTOs; empty pattern
// or rule lists are presented as JSON arrays.

// GatewaySettingsEndpoints declares HTTP operations for the gateway settings service.
type GatewaySettingsEndpoints interface {
	// GetRectifierSettings presents the shared correction settings, including an
	// array for empty pattern storage. Implementations use the request context and
	// return shared HTTP errors when settings cannot be read.
	GetRectifierSettings(*gin.Context)
	// UpdateRectifierSettings replaces correction switches and bounded cleaned
	// patterns from JSON input. Implementations save before readback, so a later
	// read error can be reported after the configuration has persisted.
	UpdateRectifierSettings(*gin.Context)
	// GetBetaPolicySettings presents ordered beta rules from the shared runtime
	// service. Implementations map those rules to HTTP DTO values and report
	// service read errors using the normal response envelope.
	GetBetaPolicySettings(*gin.Context)
	// UpdateBetaPolicySettings validates and replaces ordered beta rules supplied
	// in JSON. Implementations persist before reading the response state again; a
	// failed readback cannot imply that the preceding save was rolled back.
	UpdateBetaPolicySettings(*gin.Context)
}

// RuntimeSettingsHandler reads and replaces gateway policy settings through the
// shared service.
type RuntimeSettingsHandler struct {
	settingService *gateway.RuntimeSettings // shared gateway settings service
}

// UpdateRectifierSettingsRequest binds complete correction switches and custom
// signature patterns from JSON.
type UpdateRectifierSettingsRequest struct {
	Enabled bool `json:"enabled"` // global correction switch
	// thinking signature correction switch
	ThinkingSignatureEnabled bool `json:"thinking_signature_enabled"`
	// thinking budget correction switch
	ThinkingBudgetEnabled bool `json:"thinking_budget_enabled"`
	// API Key signature recovery switch
	APIKeySignatureEnabled bool `json:"apikey_signature_enabled"`
	// APIKeySignaturePatterns is bounded before saving: supplied entries count
	// toward fifty, blank trimmed strings are dropped, and retained strings have a
	// five-hundred-byte limit.
	APIKeySignaturePatterns []string `json:"apikey_signature_patterns"`
}

// UpdateBetaPolicySettingsRequest contains the beta policy rules to save.
type UpdateBetaPolicySettingsRequest struct {
	Rules []gatewaydto.BetaPolicyRule `json:"rules"` // ordered replacement beta rules
}

// RegisterGatewaySettingsRoutes binds rectifier and beta policy reads and
// updates below adminSettings. Assembly installs authentication and auditing on
// that parent group before registration; these routes reuse the supplied
// settings endpoint implementation.
func RegisterGatewaySettingsRoutes(adminSettings *gin.RouterGroup, endpoint GatewaySettingsEndpoints) {
	adminapi.Register(adminSettings, ManagementOperations["settings.rectifier.list"], endpoint.GetRectifierSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.rectifier.update"], endpoint.UpdateRectifierSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.beta_policy.list"], endpoint.GetBetaPolicySettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.beta_policy.update"], endpoint.UpdateBetaPolicySettings)
}

// NewRuntimeSettingsHandler binds the shared service used for gateway settings.
func NewRuntimeSettingsHandler(settings *gateway.RuntimeSettings) *RuntimeSettingsHandler {
	return &RuntimeSettingsHandler{settingService: settings}
}

// GetRectifierSettings returns stored or service-default request-correction
// settings in the HTTP success envelope. Missing pattern slices become JSON
// arrays, and service failures use shared error presentation with the request
// context.
func (h *RuntimeSettingsHandler) GetRectifierSettings(c *gin.Context) {
	settings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	patterns := settings.APIKeySignaturePatterns
	// Expose empty storage as a JSON array so the settings form can iterate
	// signature patterns using the same shape as populated responses.
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

// UpdateRectifierSettings replaces the correction switches and cleaned
// signature patterns. It bounds supplied entry count before trimming, drops
// blank patterns and limits each retained string by bytes. Saving precedes a
// readback response; a readback failure can follow successful persistence.
func (h *RuntimeSettingsHandler) UpdateRectifierSettings(c *gin.Context) {
	var req UpdateRectifierSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Bound supplied list work before cleaning; retained patterns are measured
	// in bytes after trimming so their stored text stays within the HTTP budget.
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

	// Read settings after the save to report their stored representation; failure
	// here reports unavailable readback while the preceding persistence remains
	// complete.
	updatedSettings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}

	updatedPatterns := updatedSettings.APIKeySignaturePatterns
	// Normalize empty readback storage to an array so the update response has the
	// same pattern shape as the settings read endpoint.
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

// GetBetaPolicySettings returns the shared service's ordered beta rules as HTTP
// DTO values. A zero-length list is encoded as an array; service errors are
// presented using the request context and shared response helper.
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

// UpdateBetaPolicySettings replaces the supplied ordered beta rules after
// service validation. The service can normalize whitelist strings in those
// rules, and the response reads settings again after saving; readback failure
// does not roll back persisted configuration.
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

	// Read the validated stored rules after saving so the response includes
	// service normalization; later read failure leaves the persisted replacement
	// in place.
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
