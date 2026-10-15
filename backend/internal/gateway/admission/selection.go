package admission

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// selection.go classifies provider selection failures using model support and
// scheduler rate-limit evidence. An empty provider selection can mean either
// unavailable capacity or a model that the group's configured providers do not
// support. Reporting missing model configuration requires a nonempty provider
// pool as well as absent model support; an empty pool stays a
// temporary-unavailability response. Diagnosis uses the routing model, while
// messages show the client's display model.
//
// A later scheduler error can refine temporary unavailability into rate
// limiting when its diagnostic count is positive. Missing-model diagnosis takes
// precedence over that refinement, because a rate-limit hint cannot establish
// support for an unconfigured model. Unrecognized or invalid diagnostic counts
// retain the prior diagnosis and message.

// Selection problem kinds let protocol adapters distinguish model configuration
// failures from temporary provider capacity and rate-limit failures.
const (
	SelectionUnavailable   SelectionProblemKind = iota // temporary provider unavailability
	SelectionModelNotFound                             // absent configured model support
	SelectionRateLimited                               // exhausted provider rate capacity
)

// selectionModelRateLimitedPattern extracts scheduler diagnostic counts used to
// refine a temporary selection failure into a client-facing rate-limit response.
var selectionModelRateLimitedPattern = regexp.MustCompile(`(?:model_rate_limited|rate_limited)=(\d+)`)

// SelectionProblemKind distinguishes missing model configuration from temporary capacity failures.
type SelectionProblemKind uint8

// SelectionProblem is a protocol-independent classification and message for failed provider selection.
type SelectionProblem struct {
	Kind    SelectionProblemKind // classified selection failure
	Message string               // client-facing error message
}

// DiagnoseSelection classifies a failed provider selection using the group's
// support for the routing model. It returns temporary unavailability when the
// diagnoser, group ID, or trimmed routing model is missing. An empty display
// model uses the routing model in client-facing messages.
//
// Missing-model diagnosis requires both a populated provider pool and absent
// model support. This combination distinguishes unsupported configuration from
// a group with no available candidates; all other diagnostic outcomes retain
// temporary unavailability. Platform is passed through so forced-platform entries
// diagnose the same candidate set they attempted to select.
func DiagnoseSelection(ctx context.Context, diag routing.ModelAvailabilityDiagnoser, groupID *int64, routingModel, displayModel, platform string) SelectionProblem {
	fallback := SelectionProblem{Kind: SelectionUnavailable, Message: "Service temporarily unavailable"}
	routingModel = strings.TrimSpace(routingModel)
	displayModel = strings.TrimSpace(displayModel)
	if displayModel == "" {
		displayModel = routingModel
	}
	// Incomplete diagnosis inputs cannot establish missing model configuration,
	// so this failed selection uses the client's temporary-unavailability response.
	if diag == nil || groupID == nil || routingModel == "" {
		return fallback
	}
	result := diag.DiagnoseModelAvailabilityForPlatform(ctx, groupID, routingModel, platform)
	if result.HasProvidersInPool && !result.HasModelSupport {
		return SelectionProblem{Kind: SelectionModelNotFound, Message: fmt.Sprintf("Model %q is not supported by any configured provider in this group", displayModel)}
	}
	return fallback
}

// RefineSelectionFailure refines a temporary selection problem using positive
// rate-limit counts embedded in the scheduler error. A nil error or an existing
// missing-model problem returns the supplied problem unchanged, including its
// display-model message.
//
// The scheduler's rate_limited and model_rate_limited labels share the same
// count parser. A recognized positive count produces SelectionRateLimited;
// absent labels, invalid numbers, and zero counts retain the supplied diagnosis.
// This precedence keeps unsupported-model information from being replaced by
// a capacity hint that does not establish model support.
func RefineSelectionFailure(err error, fallback SelectionProblem) SelectionProblem {
	if err == nil || fallback.Kind == SelectionModelNotFound {
		return fallback
	}
	// Scheduler counts refine capacity failures while unrelated error wording
	// retains the routing diagnosis. Accept a recognized label with a positive,
	// representable count before changing the client-facing result.
	match := selectionModelRateLimitedPattern.FindStringSubmatch(strings.ToLower(err.Error()))
	if len(match) != 2 {
		return fallback
	}
	count, parseErr := strconv.Atoi(match[1])
	if parseErr != nil || count <= 0 {
		return fallback
	}
	return SelectionProblem{Kind: SelectionRateLimited, Message: "All available providers are currently rate-limited. Please retry later."}
}
