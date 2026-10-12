package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Administrator profile updates distinguish an omitted list from an explicit
// empty array. A nil decoded list retains the stored value, while a present
// empty list reaches repository clearing and subsequent transport defaults;
// nullable scalar pointers treat JSON null like omission.
//
// The handler builds a complete profile from the current repository value
// before applying patch fields. Validation errors become bad-request responses,
// and absent reads become not-found responses. Collector routes use an optional
// listener adapter independently of profile persistence: status can return a
// disabled zero value, while control and session operations reject unavailable
// adapters. Route wiring supplies administrator protection and application
// lifecycle ownership for these retained service dependencies.

// TLSFingerprintProfileHandler binds profile persistence and optional collector
// operations for protected administrator requests.
type TLSFingerprintProfileHandler struct {
	// service is retained for administrative repository operations and refresh.
	// The application owns its startup/subscription shutdown, while the handler
	// delegates writes and reads.
	service *egress.TLSFingerprintProfileService // retained profile administration service
	// collector is optional. CollectorStatus returns a zero value when absent;
	// control and session operations instead return service-unavailable responses
	// before delegating listener work.
	collector egress.Collector // optional retained capture listener
}

// CreateTLSFingerprintProfileRequest contains fields for a new TLS profile.
//
// CipherSuites, Curves, PointFormats, SignatureAlgorithms, ALPNProtocols,
// SupportedVersions, KeyShareGroups, PSKModes, and Extensions supply ordered
// profile parameters. Empty arrays and omitted lists both reach creation
// without stored list values and use transport defaults later.
type CreateTLSFingerprintProfileRequest struct {
	Name        string  `json:"name" binding:"required"` // requested profile display name
	Description *string `json:"description"`             // optional profile description text
	// EnableGREASE changes the new profile flag when a boolean is supplied.
	// Omitted or null input leaves the constructed profile's zero flag value.
	EnableGREASE        *bool    `json:"enable_grease"`        // optional GREASE insertion flag
	CipherSuites        []uint16 `json:"cipher_suites"`        // ordered cipher suite IDs
	Curves              []uint16 `json:"curves"`               // ordered supported group IDs
	PointFormats        []uint16 `json:"point_formats"`        // ordered point format IDs
	SignatureAlgorithms []uint16 `json:"signature_algorithms"` // ordered signature scheme IDs
	ALPNProtocols       []string `json:"alpn_protocols"`       // ordered application protocol names
	SupportedVersions   []uint16 `json:"supported_versions"`   // ordered TLS version IDs
	KeyShareGroups      []uint16 `json:"key_share_groups"`     // ordered key-share group IDs
	PSKModes            []uint16 `json:"psk_modes"`            // ordered PSK exchange modes
	Extensions          []uint16 `json:"extensions"`           // ordered TLS extension IDs
}

// UpdateTLSFingerprintProfileRequest contains optional fields for a partial profile update.
//
// CipherSuites, Curves, PointFormats, SignatureAlgorithms, ALPNProtocols,
// SupportedVersions, KeyShareGroups, PSKModes, and Extensions retain stored
// values when decoded as nil. Present empty arrays replace them with empty
// lists, which repository update clears.
type UpdateTLSFingerprintProfileRequest struct {
	// Name replaces stored text when present. Omission and JSON null retain the
	// current name, while a supplied empty string reaches service validation and
	// is rejected.
	Name *string `json:"name"` // requested profile display name
	// Description replaces stored text when present, including a supplied empty
	// string. Omission and JSON null both retain the existing description rather
	// than clearing its optional value.
	Description *string `json:"description"` // optional profile description text
	// EnableGREASE replaces the stored flag when a boolean is present. Omission or
	// JSON null retains the current value, preserving false as an intentional
	// supplied update.
	EnableGREASE        *bool    `json:"enable_grease"`        // optional GREASE insertion flag
	CipherSuites        []uint16 `json:"cipher_suites"`        // ordered cipher suite IDs
	Curves              []uint16 `json:"curves"`               // ordered supported group IDs
	PointFormats        []uint16 `json:"point_formats"`        // ordered point format IDs
	SignatureAlgorithms []uint16 `json:"signature_algorithms"` // ordered signature scheme IDs
	ALPNProtocols       []string `json:"alpn_protocols"`       // ordered application protocol names
	SupportedVersions   []uint16 `json:"supported_versions"`   // ordered TLS version IDs
	KeyShareGroups      []uint16 `json:"key_share_groups"`     // ordered key-share group IDs
	PSKModes            []uint16 `json:"psk_modes"`            // ordered PSK exchange modes
	Extensions          []uint16 `json:"extensions"`           // ordered TLS extension IDs
}

// NewTLSFingerprintProfileHandler retains the profile service and first
// optional collector. Additional collectors are ignored, and an absent or nil
// first collector leaves control/session endpoints unavailable while status
// reports a zero collector value.
func NewTLSFingerprintProfileHandler(profileService *egress.TLSFingerprintProfileService, collectors ...egress.Collector) *TLSFingerprintProfileHandler {
	h := &TLSFingerprintProfileHandler{service: profileService}
	if len(collectors) > 0 {
		h.collector = collectors[0]
	}
	return h
}

// List returns persisted TLS profile values through the standard administrator
// success envelope. Repository failure uses shared error handling; stored
// missing arrays are normalized by the repository mapping before JSON
// serialization.
func (h *TLSFingerprintProfileHandler) List(c *gin.Context) {
	profiles, err := h.service.List(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, profiles)
}

// GetByID parses a signed decimal path ID and returns a persisted profile.
// Parse failure is bad request, a nil repository result is not found, and other
// repository errors use shared response handling.
func (h *TLSFingerprintProfileHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid profile ID")
		return
	}

	profile, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if profile == nil {
		response.NotFound(c, "Profile not found")
		return
	}

	response.Success(c, profile)
}

// Create binds profile JSON, builds its editable values, and delegates
// validation and persistence. Omitted GREASE input leaves false, and service
// ValidationError becomes bad request; other write failures use the standard
// error response.
func (h *TLSFingerprintProfileHandler) Create(c *gin.Context) {
	var req CreateTLSFingerprintProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	profile := &egress.TLSFingerprintProfile{
		Name:                req.Name,
		Description:         req.Description,
		CipherSuites:        req.CipherSuites,
		Curves:              req.Curves,
		PointFormats:        req.PointFormats,
		SignatureAlgorithms: req.SignatureAlgorithms,
		ALPNProtocols:       req.ALPNProtocols,
		SupportedVersions:   req.SupportedVersions,
		KeyShareGroups:      req.KeyShareGroups,
		PSKModes:            req.PSKModes,
		Extensions:          req.Extensions,
	}

	if req.EnableGREASE != nil {
		profile.EnableGREASE = *req.EnableGREASE
	}

	created, err := h.service.Create(c.Request.Context(), profile)
	if err != nil {
		if _, ok := err.(*egress.ValidationError); ok {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, created)
}

// Update reads the existing profile and applies decoded patch fields before
// submitting a complete value to persistence. Invalid decimal ID or JSON
// returns bad request; a missing profile returns not found.
//
// Nil scalar pointers and list slices retain current values, while present
// empty lists clear stored parameters and a supplied false GREASE flag remains
// an update. Description JSON null is indistinguishable from omission in this
// request type. Read and replacement are separate repository operations, so the
// assembled complete value can overwrite intervening changes by another
// administrative write.
func (h *TLSFingerprintProfileHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid profile ID")
		return
	}

	var req UpdateTLSFingerprintProfileRequest
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
		response.NotFound(c, "Profile not found")
		return
	}

	// Build a complete persistence value before applying presence-aware fields.
	// Nil decoded lists retain stored values, while explicit empty arrays reach
	// clearing through the subsequent nonnil assignment checks.
	profile := &egress.TLSFingerprintProfile{
		ID:                  id,
		Name:                existing.Name,
		Description:         existing.Description,
		EnableGREASE:        existing.EnableGREASE,
		CipherSuites:        existing.CipherSuites,
		Curves:              existing.Curves,
		PointFormats:        existing.PointFormats,
		SignatureAlgorithms: existing.SignatureAlgorithms,
		ALPNProtocols:       existing.ALPNProtocols,
		SupportedVersions:   existing.SupportedVersions,
		KeyShareGroups:      existing.KeyShareGroups,
		PSKModes:            existing.PSKModes,
		Extensions:          existing.Extensions,
	}

	if req.Name != nil {
		profile.Name = *req.Name
	}
	if req.Description != nil {
		profile.Description = req.Description
	}
	if req.EnableGREASE != nil {
		profile.EnableGREASE = *req.EnableGREASE
	}
	if req.CipherSuites != nil {
		profile.CipherSuites = req.CipherSuites
	}
	if req.Curves != nil {
		profile.Curves = req.Curves
	}
	if req.PointFormats != nil {
		profile.PointFormats = req.PointFormats
	}
	if req.SignatureAlgorithms != nil {
		profile.SignatureAlgorithms = req.SignatureAlgorithms
	}
	if req.ALPNProtocols != nil {
		profile.ALPNProtocols = req.ALPNProtocols
	}
	if req.SupportedVersions != nil {
		profile.SupportedVersions = req.SupportedVersions
	}
	if req.KeyShareGroups != nil {
		profile.KeyShareGroups = req.KeyShareGroups
	}
	if req.PSKModes != nil {
		profile.PSKModes = req.PSKModes
	}
	if req.Extensions != nil {
		profile.Extensions = req.Extensions
	}

	updated, err := h.service.Update(c.Request.Context(), profile)
	if err != nil {
		if _, ok := err.(*egress.ValidationError); ok {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, updated)
}

// Delete parses a signed decimal ID and delegates profile removal plus service
// refresh. Parse failure is bad request, write failure uses common response
// handling, and success returns the fixed administrator confirmation payload.
func (h *TLSFingerprintProfileHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid profile ID")
		return
	}

	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Profile deleted successfully"})
}

// CollectorStatus returns current collector status or a zero status value when
// no adapter is installed. It is a display query, while control/session
// operations use their separate unavailable-adapter error responses.
func (h *TLSFingerprintProfileHandler) CollectorStatus(c *gin.Context) {
	if h.collector == nil {
		response.Success(c, egress.TLSFingerprintCollectorStatus{})
		return
	}
	response.Success(c, h.collector.Status())
}

// StartCollector delegates listener startup using the administrator request
// context when an adapter is present. Missing adapter returns service
// unavailable, adapter startup failure becomes bad request, and success returns
// its effective status.
func (h *TLSFingerprintProfileHandler) StartCollector(c *gin.Context) {
	if h.collector == nil {
		response.Error(c, http.StatusServiceUnavailable, "TLS fingerprint collector is unavailable")
		return
	}
	status, err := h.collector.Start(c.Request.Context())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, status)
}

// StopCollector delegates restartable listener shutdown with the administrator
// request context. Missing adapter returns service unavailable; shutdown
// failure returns an internal error, and successful stop reads the resulting
// collector status for its response.
func (h *TLSFingerprintProfileHandler) StopCollector(c *gin.Context) {
	if h.collector == nil {
		response.Error(c, http.StatusServiceUnavailable, "TLS fingerprint collector is unavailable")
		return
	}
	if err := h.collector.Stop(c.Request.Context()); err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, h.collector.Status())
}

// CreateCollectorSession requests a temporary token, URL, expiry, and available
// trust value from the collector. Missing adapter returns service unavailable,
// a session-creation failure becomes bad request, and success returns that
// session value.
func (h *TLSFingerprintProfileHandler) CreateCollectorSession(c *gin.Context) {
	if h.collector == nil {
		response.Error(c, http.StatusServiceUnavailable, "TLS fingerprint collector is unavailable")
		return
	}
	session, err := h.collector.CreateSession()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, session)
}

// ListCollectorCaptures returns the session's retained capture records for the
// token path parameter. Missing adapter returns service unavailable;
// missing/expired session error becomes not found, and success serializes
// shared immutable records.
func (h *TLSFingerprintProfileHandler) ListCollectorCaptures(c *gin.Context) {
	if h.collector == nil {
		response.Error(c, http.StatusServiceUnavailable, "TLS fingerprint collector is unavailable")
		return
	}
	records, err := h.collector.ListCaptures(c.Param("token"))
	if err != nil {
		response.NotFound(c, err.Error())
		return
	}
	response.Success(c, records)
}

// DeleteCollectorSession asks the collector to remove the supplied token and
// returns a fixed success value. Missing adapter returns service unavailable;
// an unknown token is handled by the adapter's no-op deletion rule.
func (h *TLSFingerprintProfileHandler) DeleteCollectorSession(c *gin.Context) {
	if h.collector == nil {
		response.Error(c, http.StatusServiceUnavailable, "TLS fingerprint collector is unavailable")
		return
	}
	h.collector.DeleteSession(c.Param("token"))
	response.Success(c, gin.H{"deleted": true})
}
