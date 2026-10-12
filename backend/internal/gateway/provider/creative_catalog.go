package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativeprovider "github.com/TokenFlux/TokenRouter/internal/creative/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

type creativeCatalogProvider struct {
	creative.CatalogProvider
	policy ModelPolicy
}

func (a creativeCatalogProvider) AttributeTarget(model string) modelcatalog.Target {
	return a.policy.Record.ModelAttributeTarget(model, "")
}

// CreativeCatalogProvider resolves catalog models using execution passthrough, platform normalization, and one-hop mapping rules.
func CreativeCatalogProvider(value *provider.Record) creative.CatalogProvider {
	if value == nil {
		return nil
	}
	policy := (ModelPolicy{Record: value}).Prepared()
	return creativeCatalogProvider{CatalogProvider: creativeprovider.CatalogProviderWithRules(value, policy.models), policy: policy}
}

func (a creativeCatalogProvider) ResolveMappedModel(model string) (string, bool) {
	mapped := a.policy.UpstreamModel(context.Background(), model)
	return mapped, mapped != model
}

func (a creativeCatalogProvider) IsModelSupported(model string) bool {
	return a.policy.Supports(context.Background(), model)
}
