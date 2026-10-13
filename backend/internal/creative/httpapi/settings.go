package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Creative candidate discovery and worker status have independent service
// dependencies. Each administration handler reports a missing dependency as a
// server error, while candidate lookup failures use the common typed error
// response and successful reads share the settings envelope.

// SettingsCandidates supplies administrator creative model and group discovery
// to HTTP handlers.
type SettingsCandidates interface {
	// ListCreativeModelCandidates returns active group/model routes available for
	// administrator configuration. It applies provider and group image rules
	// rather than the administrator own group-binding permissions, and propagates
	// lookup errors.
	ListCreativeModelCandidates(context.Context) ([]creative.CreativeModelCandidate, error)
}

// SettingsHandler reads creative model candidates and worker status.
type SettingsHandler struct {
	// reader supplies administrator candidates when configured; a nil value
	// makes ListCreativeModelCandidates return its server configuration error.
	reader SettingsCandidates // bound administrator candidate reader
	// status supplies the runtime snapshot; a nil callback makes
	// GetCreativeWorkerStatus return its server configuration error before any
	// runtime read is attempted.
	status func() creative.CreativeWorkerStatus
}

// NewSettingsHandler binds candidate discovery and the worker-status callback
// for administration reads. A missing dependency is retained and its
// corresponding handler returns a server configuration error when invoked.
func NewSettingsHandler(reader SettingsCandidates, status func() creative.CreativeWorkerStatus) *SettingsHandler {
	return &SettingsHandler{reader: reader, status: status}
}

// ListCreativeModelCandidates returns the model candidate list in a success
// envelope. A nil handler or reader yields a server error, and lookup failures
// use the shared HTTP error mapping.
func (h *SettingsHandler) ListCreativeModelCandidates(c *gin.Context) {
	if h == nil || h.reader == nil {
		response.Error(c, 500, "creative model candidate service is not configured")
		return
	}
	values, err := h.reader.ListCreativeModelCandidates(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, values)
}

// GetCreativeWorkerStatus returns the current runtime snapshot from the
// configured callback. A nil handler or callback yields a server error before
// any worker status can be read.
func (h *SettingsHandler) GetCreativeWorkerStatus(c *gin.Context) {
	if h == nil || h.status == nil {
		response.Error(c, 500, "setting service is not configured")
		return
	}
	response.Success(c, h.status())
}
