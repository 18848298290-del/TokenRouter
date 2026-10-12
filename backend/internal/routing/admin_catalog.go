package routing

import (
	"sort"
)

const (
	CatalogClaude      AdminCatalogKind = "claude"
	CatalogOpenAI      AdminCatalogKind = "openai"
	CatalogGemini      AdminCatalogKind = "gemini"
	CatalogGoogleOne   AdminCatalogKind = "google_one"
	CatalogAntigravity AdminCatalogKind = "antigravity"
	CatalogQoder       AdminCatalogKind = "qoder"
	CatalogGrok        AdminCatalogKind = "grok"
)

// AdminCatalogKind identifies the administrative catalog variant.
type AdminCatalogKind string

// AdminCatalogModel contains metadata for the platform-specific HTTP catalog response.
type AdminCatalogModel struct {
	ID, Object, Type, OwnedBy, DisplayName, CreatedAt string
	Created                                           int64
}
type AdminCatalogInput struct {
	Platform, Site                string
	OAuth, GoogleOne, Passthrough bool
	Accept                        func(string) bool
}
type AdminCatalogResult struct {
	Kind   AdminCatalogKind
	Models []AdminCatalogModel
}
type AdminCatalogOptions struct {
	Lookup func(AdminCatalogKind, string) AdminCatalogModel
}

// AdminCatalog validates configured provider model IDs and looks up their display metadata.
type AdminCatalog struct{ options AdminCatalogOptions }

func NewAdminCatalog(options AdminCatalogOptions) *AdminCatalog { return &AdminCatalog{options} }
func (s *AdminCatalog) Available(input AdminCatalogInput, configured func() []string) (AdminCatalogResult, error) {
	kind := CatalogClaude
	switch input.Platform {
	case "openai":
		kind = CatalogOpenAI
	case "gemini":
		kind = CatalogGemini
		if input.GoogleOne && input.OAuth {
			kind = CatalogGoogleOne
		}
	case "antigravity":
		kind = CatalogAntigravity
	case "qoder":
		kind = CatalogQoder
	case "grok":
		kind = CatalogGrok
	}
	requested := configured()
	sort.Strings(requested)
	models := make([]AdminCatalogModel, 0, len(requested))
	seen := make(map[string]bool)
	for _, id := range requested {
		if seen[id] || (input.Accept != nil && !input.Accept(id)) {
			continue
		}
		seen[id] = true
		m := AdminCatalogModel{ID: id, Type: "model", DisplayName: id}
		if kind == CatalogOpenAI || kind == CatalogGrok {
			m.Object = "model"
		}
		if kind == CatalogGrok {
			m.Type = ""
			m.OwnedBy = "xai"
		}
		if s.options.Lookup != nil {
			m = s.options.Lookup(kind, id)
		}
		models = append(models, m)
	}
	return AdminCatalogResult{Kind: kind, Models: models}, nil
}
