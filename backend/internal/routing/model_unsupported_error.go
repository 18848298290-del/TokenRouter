package routing

import (
	"fmt"
	"strings"
)

const groupModelUnsupportedAvailableModelsLimit = 20

// GroupModelUnsupportedError reports that the group has no schedulable provider supporting the requested model.
type GroupModelUnsupportedError struct {
	Platform        string
	RequestedModel  string
	AvailableModels []string
}

func (e *GroupModelUnsupportedError) Error() string {
	if e == nil {
		return ""
	}
	return buildGroupModelUnsupportedMessage(e.RequestedModel, truncateModelListForError(e.AvailableModels))
}

// buildGroupModelUnsupportedMessage formats the public English error message.
func buildGroupModelUnsupportedMessage(requestedModel string, availableModels []string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	message := fmt.Sprintf("The current group does not support the requested model %q", requestedModel)
	if len(availableModels) == 0 {
		return message
	}
	return fmt.Sprintf("%s. Available models: %s", message, strings.Join(availableModels, ", "))
}

// truncateModelListForError bounds model suggestions and appends the omitted count.
func truncateModelListForError(models []string) []string {
	if len(models) <= groupModelUnsupportedAvailableModelsLimit {
		return append([]string(nil), models...)
	}
	out := append([]string(nil), models[:groupModelUnsupportedAvailableModelsLimit]...)
	out = append(out, fmt.Sprintf("and %d more", len(models)-groupModelUnsupportedAvailableModelsLimit))
	return out
}
