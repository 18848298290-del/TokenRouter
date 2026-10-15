package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/tlsfingerprintrouter"
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// tls_router_repo.go persists TLS routers and converts their rules and
// dedicated profile selections.
// Stored TLS routers keep ordered User-Agent rules alongside dedicated token
// and reset identity settings. Entity mapping supplies an empty Rules array for
// administrator responses when storage is absent, while update clears omitted
// nullable selections because it receives a complete merged router value.

// tlsFingerprintRouterRepository maps Ent router persistence into egress
// configuration values and ordered rules.
type tlsFingerprintRouterRepository struct {
	// client is retained for router administration and cache reload queries. The
	// application owns closing persistence after its consumers and
	// subscription-driven refresh callbacks have completed.
	client *ent.Client // borrowed application Ent client
}

// NewTLSFingerprintRouterRepository creates a router adapter retaining the
// supplied Ent client. Its application owner keeps that client available while
// administrative writes, runtime misses, and cache reload callbacks use the
// adapter.
func NewTLSFingerprintRouterRepository(client *ent.Client) egress.TLSFingerprintRouterRepository {
	return &tlsFingerprintRouterRepository{client: client}
}

// List returns persisted routers ordered by stored name and maps their ordered
// rules into egress values. Optional pointers and populated rule slices remain
// associated with loaded entities until runtime publication clones them.
func (r *tlsFingerprintRouterRepository) List(ctx context.Context) ([]*egress.TLSFingerprintRouter, error) {
	routers, err := r.client.TLSFingerprintRouter.Query().
		Order(ent.Asc(tlsfingerprintrouter.FieldName)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*egress.TLSFingerprintRouter, len(routers))
	for i, router := range routers {
		result[i] = r.toModel(router)
	}
	return result, nil
}

// GetByID returns the stored router, or nil with no error when its ID is
// missing. Other persistence errors propagate, and absent Rules is converted to
// a present empty slice for responses.
func (r *tlsFingerprintRouterRepository) GetByID(ctx context.Context, id int64) (*egress.TLSFingerprintRouter, error) {
	router, err := r.client.TLSFingerprintRouter.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toModel(router), nil
}

// Create stores the supplied router identity settings, enabled flag, and
// ordered rules, including configured nullable values. It returns persisted
// metadata through entity mapping; absent Rules is represented as a present
// empty slice.
func (r *tlsFingerprintRouterRepository) Create(ctx context.Context, router *egress.TLSFingerprintRouter) (*egress.TLSFingerprintRouter, error) {
	builder := r.client.TLSFingerprintRouter.Create().
		SetName(router.Name).
		SetEnabled(router.Enabled).
		SetChatgptOauthTokenUserAgent(router.ChatGPTOAuthTokenUserAgent).
		SetCodexInviteResetUserAgent(router.CodexInviteResetUserAgent).
		SetRules(router.Rules)
	if router.Description != nil {
		builder.SetDescription(*router.Description)
	}
	if router.ChatGPTOAuthTokenTLSFingerprintProfileID != nil {
		builder.SetChatgptOauthTokenTLSFingerprintProfileID(*router.ChatGPTOAuthTokenTLSFingerprintProfileID)
	}
	if router.CodexInviteResetTLSFingerprintProfileID != nil {
		builder.SetCodexInviteResetTLSFingerprintProfileID(*router.CodexInviteResetTLSFingerprintProfileID)
	}
	created, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(created), nil
}

// Update replaces stored router fields by ID, clearing absent description and
// dedicated profile selections. It receives a complete desired router after
// HTTP patch merging, so omitted nullable values here mean stored removal.
func (r *tlsFingerprintRouterRepository) Update(ctx context.Context, router *egress.TLSFingerprintRouter) (*egress.TLSFingerprintRouter, error) {
	builder := r.client.TLSFingerprintRouter.UpdateOneID(router.ID).
		SetName(router.Name).
		SetEnabled(router.Enabled).
		SetChatgptOauthTokenUserAgent(router.ChatGPTOAuthTokenUserAgent).
		SetCodexInviteResetUserAgent(router.CodexInviteResetUserAgent).
		SetRules(router.Rules)
	// The HTTP layer has already merged patch presence into a complete value.
	// Clear nil optional selections so an explicit patch null reaches stored removal.
	if router.Description != nil {
		builder.SetDescription(*router.Description)
	} else {
		builder.ClearDescription()
	}
	if router.ChatGPTOAuthTokenTLSFingerprintProfileID != nil {
		builder.SetChatgptOauthTokenTLSFingerprintProfileID(*router.ChatGPTOAuthTokenTLSFingerprintProfileID)
	} else {
		builder.ClearChatgptOauthTokenTLSFingerprintProfileID()
	}
	if router.CodexInviteResetTLSFingerprintProfileID != nil {
		builder.SetCodexInviteResetTLSFingerprintProfileID(*router.CodexInviteResetTLSFingerprintProfileID)
	} else {
		builder.ClearCodexInviteResetTLSFingerprintProfileID()
	}
	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(updated), nil
}

// Delete removes the stored TLS router record identified by id.
func (r *tlsFingerprintRouterRepository) Delete(ctx context.Context, id int64) error {
	return r.client.TLSFingerprintRouter.DeleteOneID(id).Exec(ctx)
}

// toModel maps a nonnil Ent router and supplies a present empty Rules slice
// when absent. Description and dedicated profile pointers, plus populated
// rules, retain entity storage until runtime services clone the mapped value.
func (r *tlsFingerprintRouterRepository) toModel(e *ent.TLSFingerprintRouter) *egress.TLSFingerprintRouter {
	router := &egress.TLSFingerprintRouter{
		ID:                                       e.ID,
		Name:                                     e.Name,
		Description:                              e.Description,
		Enabled:                                  e.Enabled,
		ChatGPTOAuthTokenUserAgent:               e.ChatgptOauthTokenUserAgent,
		ChatGPTOAuthTokenTLSFingerprintProfileID: e.ChatgptOauthTokenTLSFingerprintProfileID,
		CodexInviteResetUserAgent:                e.CodexInviteResetUserAgent,
		CodexInviteResetTLSFingerprintProfileID:  e.CodexInviteResetTLSFingerprintProfileID,
		Rules:                                    e.Rules,
		CreatedAt:                                e.CreatedAt,
		UpdatedAt:                                e.UpdatedAt,
	}
	// Keep the administrator response collection shape as an array when the
	// entity has no stored rules, before later runtime cloning takes owned storage.
	if router.Rules == nil {
		router.Rules = []egress.TLSFingerprintRouterRule{}
	}
	return router
}
