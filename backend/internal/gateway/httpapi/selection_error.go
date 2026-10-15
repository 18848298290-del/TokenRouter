package httpapi

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// selection_error.go converts provider selection diagnoses into HTTP failures
// and refines existing responses with concrete attempt errors. Admission
// diagnosis distinguishes unavailable models, rate limits and exhausted
// capacity; the HTTP mapping assigns the corresponding status, category and
// client message.
//
// Refinement reconstructs the diagnostic kind from the existing ModelNotFound
// flag and asks admission to interpret the concrete failure. An unchanged kind
// returns the original response, retaining its customized status and message.
// A changed kind receives a newly mapped response. Callers can therefore
// retain a specific unavailable response while still allowing a concrete
// failure to change its classification.

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
