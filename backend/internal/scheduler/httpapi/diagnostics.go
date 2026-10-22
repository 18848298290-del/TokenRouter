package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// diagnostics.go exposes administrative HTTP endpoints for scheduler score
// summaries and previews.
// Administrative score requests identify a provider and optionally one
// advanced group. Overview reads and detailed previews share the diagnostic
// service, while preview decoding accepts a single known-field JSON object so
// request-only session state cannot enter the simulation.

// ProviderSchedulerDiagnostics supplies provider group summaries and requested
// score explanations for administrative reads.
type ProviderSchedulerDiagnostics interface {
	// GetOverview returns the provider's advanced-group summaries under the
	// request context. The handler delegates diagnostic errors to HTTP error
	// conversion and returns the resulting score data through the standard
	// success envelope.
	GetOverview(context.Context, int64) (*policy.AdvancedSchedulerScoreDiagnosticResponse, error)
	// GetDetail returns one advanced group's score explanation for the supplied
	// model and provider IDs. The handler maps recognized input-validation
	// messages to a bad request while forwarding other errors through standard
	// conversion.
	GetDetail(context.Context, int64, policy.AdvancedSchedulerScoreDiagnosticRequest) (*policy.AdvancedSchedulerScoreDiagnosticResponse, error)
}

// DiagnosticsHandler maps administrative HTTP input to shared scheduler score
// diagnostic reads.
type DiagnosticsHandler struct{ diagnostics ProviderSchedulerDiagnostics } // shared score reader

// NewDiagnosticsHandler binds the supplied diagnostic reader to administrative
// score endpoints. A nil reader is retained, letting endpoint methods report
// unavailable diagnostics after they validate the requested provider ID.
func NewDiagnosticsHandler(source ProviderSchedulerDiagnostics) *DiagnosticsHandler {
	return &DiagnosticsHandler{diagnostics: source}
}

// GetAdvancedSchedulerScore returns advanced-group summaries or a baseline
// explanation for one positive group ID. It validates the provider first, then
// reports an unavailable source or decodes the optional group query;
// recognized diagnostic input errors become bad requests.
func (h *DiagnosticsHandler) GetAdvancedSchedulerScore(c *gin.Context) {
	providerID, ok := parseAdvancedSchedulerScoreProviderID(c)
	if !ok {
		return
	}
	if h == nil || h.diagnostics == nil {
		response.InternalError(c, "Advanced scheduler diagnostics are unavailable")
		return
	}

	groupIDRaw := strings.TrimSpace(c.Query("group_id"))
	if groupIDRaw == "" {
		result, err := h.diagnostics.GetOverview(c.Request.Context(), providerID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, result)
		return
	}
	groupID, err := strconv.ParseInt(groupIDRaw, 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	result, err := h.diagnostics.GetDetail(c.Request.Context(), providerID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: groupID})
	if err != nil {
		// The diagnostic service returns ordinary errors for invalid simulation
		// inputs. Map the known validation messages to a client error while
		// retaining standard conversion for other failures.
		if strings.Contains(err.Error(), "advanced scheduler group") || strings.Contains(err.Error(), "group_id") {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// PreviewAdvancedSchedulerScore returns an advanced-group explanation using
// administrator-supplied model and provider IDs. It requires one JSON object
// containing known request fields and a positive group ID, then converts
// input-related diagnostic errors to bad requests.
func (h *DiagnosticsHandler) PreviewAdvancedSchedulerScore(c *gin.Context) {
	providerID, ok := parseAdvancedSchedulerScoreProviderID(c)
	if !ok {
		return
	}
	if h == nil || h.diagnostics == nil {
		response.InternalError(c, "Advanced scheduler diagnostics are unavailable")
		return
	}

	var request policy.AdvancedSchedulerScoreDiagnosticRequest
	decoder := json.NewDecoder(c.Request.Body)
	// Preview inputs intentionally carry model and provider IDs. Reject unknown
	// fields so session hashes or other request state cannot silently enter this simulation API.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		response.BadRequest(c, "Invalid advanced scheduler score preview request: "+err.Error())
		return
	}
	if err := ensureAdvancedSchedulerScorePreviewEOF(decoder); err != nil {
		response.BadRequest(c, "Invalid advanced scheduler score preview request: "+err.Error())
		return
	}
	if request.GroupID <= 0 {
		response.BadRequest(c, "group_id is required")
		return
	}

	result, err := h.diagnostics.GetDetail(c.Request.Context(), providerID, request)
	if err != nil {
		// The diagnostic service returns ordinary errors for invalid simulation
		// inputs. Map the known validation messages to a client error while
		// retaining standard conversion for other failures.
		if strings.Contains(err.Error(), "advanced scheduler group") || strings.Contains(err.Error(), "group_id") || strings.Contains(err.Error(), "sticky provider") || strings.Contains(err.Error(), "requested_model") {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// parseAdvancedSchedulerScoreProviderID reads a positive decimal provider ID
// from the route. Invalid text writes the standard bad-request response and
// returns false, so callers stop before invoking diagnostic reads.
func parseAdvancedSchedulerScoreProviderID(c *gin.Context) (int64, bool) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || providerID <= 0 {
		response.BadRequest(c, "Invalid provider ID")
		return 0, false
	}
	return providerID, true
}

// ensureAdvancedSchedulerScorePreviewEOF verifies that the preview object is
// the body's complete JSON input. A second decoded value returns
// io.ErrUnexpectedEOF, while malformed trailing input returns its decode error
// for bad-request reporting.
func ensureAdvancedSchedulerScorePreviewEOF(decoder *json.Decoder) error {
	var extra any
	// One preview body names one scoring context. Decode again to reject a
	// second object or malformed suffix that would otherwise be left unread after the accepted request.
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

// RegisterProviderDiagnostics installs overview and preview routes on the
// supplied provider router group. The application supplies the group's
// authorization middleware and keeps the bound endpoint available for later
// requests.
func RegisterProviderDiagnostics(providers *gin.RouterGroup, endpoint *DiagnosticsHandler) {
	adminapi.Register(providers, ManagementOperations["providers.advanced_scheduler_score.list"], endpoint.GetAdvancedSchedulerScore)
	adminapi.Register(providers, ManagementOperations["providers.advanced_scheduler_score.preview.create"], endpoint.PreviewAdvancedSchedulerScore)
}
