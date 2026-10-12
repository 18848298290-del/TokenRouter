package admission

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

const (
	SelectionUnavailable SelectionProblemKind = iota
	SelectionModelNotFound
	SelectionRateLimited
)

var selectionModelRateLimitedPattern = regexp.MustCompile(`(?:model_rate_limited|rate_limited)=(\d+)`)

// SelectionProblemKind distinguishes missing model configuration from temporary capacity failures.
type SelectionProblemKind uint8

// SelectionProblem contains a client-facing message using the model name visible to the client.
type SelectionProblem struct {
	Kind    SelectionProblemKind
	Message string
}

// DiagnoseSelection checks model support in a group. Missing group or model input yields temporary unavailability.
func DiagnoseSelection(ctx context.Context, diag routing.ModelAvailabilityDiagnoser, groupID *int64, routingModel, displayModel, platform string) SelectionProblem {
	fallback := SelectionProblem{Kind: SelectionUnavailable, Message: "Service temporarily unavailable"}
	routingModel = strings.TrimSpace(routingModel)
	displayModel = strings.TrimSpace(displayModel)
	if displayModel == "" {
		displayModel = routingModel
	}
	if diag == nil || groupID == nil || routingModel == "" {
		return fallback
	}
	result := diag.DiagnoseModelAvailabilityForPlatform(ctx, groupID, routingModel, platform)
	if result.HasProvidersInPool && !result.HasModelSupport {
		return SelectionProblem{Kind: SelectionModelNotFound, Message: fmt.Sprintf("Model %q is not supported by any configured provider in this group", displayModel)}
	}
	return fallback
}

// RefineSelectionFailure retains model-configuration errors, otherwise refining failures from scheduler rate-limit counts.
func RefineSelectionFailure(err error, fallback SelectionProblem) SelectionProblem {
	if err == nil || fallback.Kind == SelectionModelNotFound {
		return fallback
	}
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
