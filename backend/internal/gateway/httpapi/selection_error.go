package httpapi

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Provider selection failures distinguish unavailable models, rate limits and
// exhausted capacity. Admission diagnosis supplies a client-facing problem, and
// HTTP mapping selects its status and category; concrete failure refinement
// retains a customized fallback when its diagnosed kind remains unchanged.

// SelectionErrorResponse holds the status, category, and message for a selection failure.
type SelectionErrorResponse struct {
	Status int // client HTTP status code
	// ErrType names the protocol category; Message holds the client failure
	// description.
	ErrType, Message string
	ModelNotFound    bool // model-configuration failure classification
}

// selectionErrorResponse maps admission problem kinds to client status and
// category. Missing models use 404, rate limiting uses 429 and remaining kinds
// use 503; the supplied problem message is retained in each result.
func selectionErrorResponse(problem admission.SelectionProblem) SelectionErrorResponse {
	switch problem.Kind {
	case admission.SelectionModelNotFound:
		return SelectionErrorResponse{Status: http.StatusNotFound, ErrType: "model_not_found", Message: problem.Message, ModelNotFound: true}
	case admission.SelectionRateLimited:
		return SelectionErrorResponse{Status: http.StatusTooManyRequests, ErrType: "rate_limit_error", Message: problem.Message}
	default:
		return SelectionErrorResponse{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: problem.Message}
	}
}

// ClassifySelectionError diagnoses provider availability for the requested
// group, route and display model. It maps the admission result to an HTTP
// response, leaving availability fallback and safe client-message selection
// with the admission implementation.
func ClassifySelectionError(ctx context.Context, diag routing.ModelAvailabilityDiagnoser, groupID *int64, routingModel, displayModel, platform string) SelectionErrorResponse {
	return selectionErrorResponse(admission.DiagnoseSelection(ctx, diag, groupID, routingModel, displayModel, platform))
}

// RefineSelectionError refines the diagnosed fallback using concrete selection
// failure evidence. If its problem kind stays unchanged, the complete supplied
// response wins, including its custom status and type; a changed kind receives
// the standard mapped response.
func RefineSelectionError(err error, fallback SelectionErrorResponse) SelectionErrorResponse {
	kind := admission.SelectionUnavailable
	if fallback.ModelNotFound {
		kind = admission.SelectionModelNotFound
	}
	problem := admission.RefineSelectionFailure(err, admission.SelectionProblem{Kind: kind, Message: fallback.Message})
	if problem.Kind == kind {
		return fallback
	}
	return selectionErrorResponse(problem)
}
