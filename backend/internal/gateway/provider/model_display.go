package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeldisplay"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// ModelDisplayCatalogue converts unified catalogue entries into client-facing display fields.
type ModelDisplayCatalogue struct{ Source modelcatalog.Reader }

func (c ModelDisplayCatalogue) ModelAttributes(model string) modelcatalog.Attributes {
	if c.Source == nil {
		return modelcatalog.Attributes{}
	}
	return c.Source.ModelEntry(model).Attributes
}

// Model keeps the requested model ID and looks up its full identity for the display name and vendor.
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

// GeminiModel formats a Gemini model resource. Callers check protocol eligibility first.
func (c ModelDisplayCatalogue) GeminiModel(id string, _ bool) modeldisplay.GeminiModel {
	id = strings.TrimPrefix(id, "models/")
	return modeldisplay.GeminiModel{Name: "models/" + id, DisplayName: c.Model(id).DisplayName, SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"}}
}
