package modeldisplay

import (
	"strings"
)

// Model-list entries retain the identifier a client can request even when
// catalogue metadata supplies another display label or owner. Protocol
// response shapes choose their own names and optional fields; merging
// candidate identifiers keeps primary order while trimming blanks and removing
// duplicate names.

// Catalog looks up requested model IDs; protocol adapters choose the response
// fields.
type Catalog interface {
	// Model returns display metadata while retaining the supplied requestable model
	// identifier. Callers qualify catalogue eligibility before asking the selected
	// adapter to format an entry for their protocol.
	Model(string) OpenAIModel
	// GeminiModel formats a model resource with the caller's forced-Antigravity
	// context flag. The production ModelDisplayCatalogue accepts that flag but
	// uses the same metadata shape for either value.
	GeminiModel(string, bool) GeminiModel
}

// ClaudeModel is the Anthropic model-list shape with a requestable identifier.
type ClaudeModel struct {
	ID          string `json:"id"`           // requestable model identifier
	Type        string `json:"type"`         // protocol model type label
	DisplayName string `json:"display_name"` // client-facing model display label
	CreatedAt   string `json:"created_at"`   // protocol creation time string
}

// OpenAIModel holds requestable identity and catalogue display metadata for
// compatible listings.
type OpenAIModel struct {
	ID          string `json:"id"`           // requestable model identifier
	Object      string `json:"object"`       // protocol object type label
	Created     int64  `json:"created"`      // protocol creation timestamp value
	OwnedBy     string `json:"owned_by"`     // display model owner label
	Type        string `json:"type"`         // protocol model type label
	DisplayName string `json:"display_name"` // client-facing model display label
}

// GrokModel holds Grok listing metadata with optional type and creation
// fields.
type GrokModel struct {
	ID          string `json:"id"`                     // requestable model identifier
	Object      string `json:"object"`                 // protocol object type label
	Type        string `json:"type,omitempty"`         // protocol model type label
	Created     int64  `json:"created,omitempty"`      // protocol creation timestamp value
	OwnedBy     string `json:"owned_by"`               // display model owner label
	DisplayName string `json:"display_name,omitempty"` // client-facing model display label
}

// GeminiModel is a Gemini resource name and its generation display metadata.
type GeminiModel struct {
	Name                       string   `json:"name"`                                 // Google model resource name
	DisplayName                string   `json:"displayName,omitempty"`                // client-facing model display label
	Description                string   `json:"description,omitempty"`                // optional catalogue model description
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"` // advertised generation method names
}

// GeminiModelsList is the Google protocol envelope for a model collection.
type GeminiModelsList struct {
	Models []GeminiModel `json:"models"` // formatted model resource entries
}

// MergeModelIDs returns trimmed, nonblank identifiers in first-seen order
// across primary then secondary. It removes exact duplicates after trimming
// and allocates new result storage; empty input produces an empty slice.
func MergeModelIDs(primary, secondary []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	merged := make([]string, 0, len(primary)+len(secondary))
	for _, models := range [][]string{primary, secondary} {
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			merged = append(merged, model)
		}
	}
	return merged
}
