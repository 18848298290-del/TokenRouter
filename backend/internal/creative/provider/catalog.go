package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// catalog.go adapts provider records and prepared routing rules for creative
// model discovery.
// Creative model discovery evaluates the same prepared provider mappings and
// protocol routes used by gateway selection. Provider records retain their
// current scheduling flags, while captured model and protocol rules determine
// which configured names can enter a creative query.

// catalogProvider combines a retained provider record with prepared model and
// protocol rules.
type catalogProvider struct {
	// Record retains scheduling data; models and protocols capture the rules read
	// during construction. Callers keep the retained provider record stable while
	// a creative query evaluates its candidate names.
	*provider.Record                              // retained provider scheduling record
	models           *provider.ModelRulesSnapshot // prepared provider model rules
	protocols        capability.ProviderProtocols // captured provider native protocols
}

// CatalogProvider prepares gateway model rules for creative discovery from a
// supplied provider record. Nil records return a nil creative provider; nonnil
// records remain shared with the returned adapter.
func CatalogProvider(value *provider.Record) creative.CatalogProvider {
	if value == nil {
		return nil
	}
	return CatalogProviderWithRules(value, provider.PrepareModelRules(value, provideradapter.ModelDefaults(), provideradapter.ModelRules(value)))
}

// CatalogProviderWithRules binds a nonnil provider record and prepared rules
// to creative queries. The record and model snapshot are retained, while
// protocol routing data is captured from the record routing snapshot.
func CatalogProviderWithRules(value *provider.Record, rules *provider.ModelRulesSnapshot) creative.CatalogProvider {
	return catalogProvider{Record: value, models: rules, protocols: value.RoutingSnapshot().Protocols()}
}

// GetModelMapping returns a copied map of this provider prepared model
// mappings. Callers can edit the returned map independently from the retained
// rules used by later lookups.
func (a catalogProvider) GetModelMapping() map[string]string {
	return a.models.Mapping()
}

// GetConfiguredRequestModels returns a copied list of finite configured
// provider names. The prepared snapshot supplies sorted candidates, and edits
// to the returned elements leave later rule reads unchanged.
func (a catalogProvider) GetConfiguredRequestModels() []string {
	return a.models.ConfiguredModels()
}

// IsModelSupported reports whether the prepared provider rules admit the
// supplied request model.
func (a catalogProvider) IsModelSupported(model string) bool {
	return a.models.Supports(model)
}

// ResolveMappedModel applies one prepared provider mapping and reports whether
// a rule matched. An unmatched input retains its spelling; exact and wildcard
// matching precedence belongs to ModelRulesSnapshot.
func (a catalogProvider) ResolveMappedModel(model string) (string, bool) {
	return a.models.ResolveMappedModel(model)
}

// PlatformID returns the retained provider record platform used by creative
// route selection.
func (a catalogProvider) PlatformID() string { return a.Platform }

// AllowsProtocol reports whether the captured native protocols accept source
// under supplied group fallbacks. Route resolution applies the capability
// rules and leaves the captured protocol snapshot unchanged.
func (a catalogProvider) AllowsProtocol(source protocol.ProtocolID, fallbacks map[protocol.ProtocolID][]protocol.ProtocolID) bool {
	_, ok := capability.ResolveRoute(a.protocols, source, fallbacks)
	return ok
}
