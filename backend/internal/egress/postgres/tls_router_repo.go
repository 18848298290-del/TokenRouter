package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/tlsfingerprintrouter"
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

type tlsFingerprintRouterRepository struct {
	client *ent.Client
}

// NewTLSFingerprintRouterRepository creates a TLS router repository.
func NewTLSFingerprintRouterRepository(client *ent.Client) egress.TLSFingerprintRouterRepository {
	return &tlsFingerprintRouterRepository{client: client}
}

// List returns all TLS routers ordered by name.
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

// GetByID returns a TLS router, or nil when the ID is missing.
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

// Create persists a TLS router.
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

// Update replaces a TLS router's fields, clearing absent optional values.
func (r *tlsFingerprintRouterRepository) Update(ctx context.Context, router *egress.TLSFingerprintRouter) (*egress.TLSFingerprintRouter, error) {
	builder := r.client.TLSFingerprintRouter.UpdateOneID(router.ID).
		SetName(router.Name).
		SetEnabled(router.Enabled).
		SetChatgptOauthTokenUserAgent(router.ChatGPTOAuthTokenUserAgent).
		SetCodexInviteResetUserAgent(router.CodexInviteResetUserAgent).
		SetRules(router.Rules)
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

// Delete deletes a TLS router.
func (r *tlsFingerprintRouterRepository) Delete(ctx context.Context, id int64) error {
	return r.client.TLSFingerprintRouter.DeleteOneID(id).Exec(ctx)
}

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
	if router.Rules == nil {
		router.Rules = []egress.TLSFingerprintRouterRule{}
	}
	return router
}
