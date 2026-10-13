package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// Client model resources retain the requested ID while display metadata is
// looked up by its full identity. Unknown entries keep the ID as the display
// name and tokenrouter as ownership; Gemini formatting removes one models/
// prefix and exposes the two generation methods after eligibility is checked
// elsewhere.

// ModelDisplayCatalogue adapts unified model metadata to client resource
// display values.
type ModelDisplayCatalogue struct {
	Source modelcatalog.Reader /* optional unified metadata reader */
}

// ModelAttributes retrieves attributes for the supplied full model ID from
// Source. An absent reader returns unknown attributes, so callers can
// construct model resources when display metadata is unavailable.
func (c ModelDisplayCatalogue) ModelAttributes(model string) modelcatalog.Attributes {
	if c.Source == nil {
		return modelcatalog.Attributes{}
	}
	return c.Source.ModelEntry(model).Attributes
}

// Model constructs a client model resource retaining id as its request
// identity. Source provider and nonblank display-name metadata replace
// defaults when available; missing metadata keeps tokenrouter ownership and id
// as the name.
func (c ModelDisplayCatalogue) Model(id string) modeldisplay.OpenAIModel {
	model := modeldisplay.OpenAIModel{ID: id, Object: "model", Type: "model", OwnedBy: "tokenrouter", DisplayName: id}
	if c.Source != nil {
		entry := c.Source.ModelEntry(id)
		if entry.Provider != "" {
			model.OwnedBy = entry.Provider
		}
		if entry.Attributes.DisplayName != nil && strings.TrimSpace(*entry.Attributes.DisplayName) != "" {
			model.DisplayName = *entry.Attributes.DisplayName
		}
	}
	return model
}

// GeminiModel formats a Gemini resource with generateContent and
// streamGenerateContent after stripping one models/ prefix. Callers check
// protocol eligibility before using it; the compatibility boolean argument
// currently has no effect.
func (c ModelDisplayCatalogue) GeminiModel(id string, _ bool) modeldisplay.GeminiModel {
	id = strings.TrimPrefix(id, "models/")
	return modeldisplay.GeminiModel{Name: "models/" + id, DisplayName: c.Model(id).DisplayName, SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"}}
}
