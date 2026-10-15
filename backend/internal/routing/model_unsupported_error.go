package routing

import (
	"fmt"
	"strings"
)

// model_unsupported_error.go formats group-model rejection errors with bounded
// alternative-model lists.
// Group-model rejection messages include concrete alternatives when suggestion
// building finds them. Public text trims the requested name and bounds the
// displayed list while reporting how many suggestions were omitted, so large
// provider catalogues cannot produce an unbounded error message.

// groupModelUnsupportedAvailableModelsLimit bounds concrete suggestions in
// public error text, with an extra suffix recording the omitted count.
const groupModelUnsupportedAvailableModelsLimit = 20

// GroupModelUnsupportedError contains a rejected request identity and its
// group-scoped suggestion list.
type GroupModelUnsupportedError struct {
	Platform        string   // provider platform diagnostic scope
	RequestedModel  string   // client model reported rejected
	AvailableModels []string // concrete group model suggestions
}

// Error formats the requested model and bounded suggestions for public
// rejection text. A nil receiver returns an empty string, and formatting leaves
// the supplied AvailableModels slice unchanged.
func (e *GroupModelUnsupportedError) Error() string {
	if e == nil {
		return ""
	}
	return buildGroupModelUnsupportedMessage(e.RequestedModel, truncateModelListForError(e.AvailableModels))
}

// buildGroupModelUnsupportedMessage returns rejection text after trimming
// requestedModel's outer whitespace. Empty suggestions omit the alternatives
// clause; callers bound suggestion count before invoking this formatter.
// Suggestion spellings are retained.
func buildGroupModelUnsupportedMessage(requestedModel string, availableModels []string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	message := fmt.Sprintf("The current group does not support the requested model %q", requestedModel)
	if len(availableModels) == 0 {
		return message
	}
	return fmt.Sprintf("%s. Available models: %s", message, strings.Join(availableModels, ", "))
}

// truncateModelListForError copies suggestions and limits the returned list to
// twenty names plus an omitted-count suffix. Short nonempty inputs receive
// separate storage, and every empty input returns nil.
func truncateModelListForError(models []string) []string {
	if len(models) <= groupModelUnsupportedAvailableModelsLimit {
		return append([]string(nil), models...)
	}
	// Bound public error size while retaining the omitted-count summary.
	out := append([]string(nil), models[:groupModelUnsupportedAvailableModelsLimit]...)
	out = append(out, fmt.Sprintf("and %d more", len(models)-groupModelUnsupportedAvailableModelsLimit))
	return out
}
