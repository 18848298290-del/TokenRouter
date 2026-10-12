package routing

import "context"

// RequestableCatalogue resolves group catalogues and selected models from current provider configuration.
type RequestableCatalogue struct {
	Read     func(context.Context, *int64) ([]CatalogueProvider, error)
	Resolver RequestableResolver
	Warn     func(string, ...any)
}

func (c *RequestableCatalogue) Prefetch(ctx context.Context) ([]CatalogueProvider, bool, error) {
	if c == nil || c.Read == nil {
		return nil, false, nil
	}
	values, err := c.Read(ctx, nil)
	return values, err == nil, err
}

// ResolveRequestableModels loads current providers and validates configured candidates; read failures produce a
// restricted empty result.
func (c *RequestableCatalogue) ResolveRequestableModels(ctx context.Context, groupID *int64, platform string) RequestableModelsResult {
	if c == nil || c.Read == nil {
		return RequestableModelsResult{}
	}
	providers, err := c.Read(ctx, groupID)
	if err != nil {
		if c.Warn != nil {
			var id int64
			if groupID != nil {
				id = *groupID
			}
			c.Warn("failed to load providers for requestable model resolution", "group_id", id, "platform", platform, "error", err)
		}
		return RequestableModelsResult{Restricted: true}
	}
	return c.Resolver.ResolveWithProviders(ctx, groupID, platform, nil, providers)
}

// ResolveSelectedModels validates selected names and alias targets against one prepared set of rules.
func (c *RequestableCatalogue) ResolveSelectedModels(ctx context.Context, groupID *int64, platform string, models []string) RequestableModelsResult {
	if c == nil || c.Read == nil {
		return RequestableModelsResult{}
	}
	providers, err := c.Read(ctx, groupID)
	if err != nil {
		return RequestableModelsResult{Restricted: true}
	}
	return c.Resolver.ResolveSelectedWithProviders(ctx, groupID, platform, models, providers)
}
