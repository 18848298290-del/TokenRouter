package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/errorpassthroughrule"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
)

type errorPassthroughRepository struct {
	client *ent.Client
}

// NewErrorPassthroughRepository creates the error passthrough rule repository.
func NewErrorPassthroughRepository(client *ent.Client) errorpolicy.ErrorPassthroughRepository {
	return &errorPassthroughRepository{client: client}
}

// List returns all rules in ascending priority order.
func (r *errorPassthroughRepository) List(ctx context.Context) ([]*errorpolicy.ErrorPassthroughRule, error) {
	rules, err := r.client.ErrorPassthroughRule.Query().
		Order(ent.Asc(errorpassthroughrule.FieldPriority)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]*errorpolicy.ErrorPassthroughRule, len(rules))
	for i, rule := range rules {
		result[i] = r.toModel(rule)
	}
	return result, nil
}

// GetByID returns a rule, or nil when the ID is absent.
func (r *errorPassthroughRepository) GetByID(ctx context.Context, id int64) (*errorpolicy.ErrorPassthroughRule, error) {
	rule, err := r.client.ErrorPassthroughRule.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toModel(rule), nil
}

// Create persists an error passthrough rule.
func (r *errorPassthroughRepository) Create(ctx context.Context, rule *errorpolicy.ErrorPassthroughRule) (*errorpolicy.ErrorPassthroughRule, error) {
	builder := r.client.ErrorPassthroughRule.Create().
		SetName(rule.Name).
		SetEnabled(rule.Enabled).
		SetPriority(rule.Priority).
		SetMatchMode(rule.MatchMode).
		SetPassthroughCode(rule.PassthroughCode).
		SetPassthroughBody(rule.PassthroughBody).
		SetSkipMonitoring(rule.SkipMonitoring)

	if len(rule.ErrorCodes) > 0 {
		builder.SetErrorCodes(rule.ErrorCodes)
	}
	if len(rule.Keywords) > 0 {
		builder.SetKeywords(rule.Keywords)
	}
	if len(rule.Platforms) > 0 {
		builder.SetPlatforms(rule.Platforms)
	}
	if rule.ResponseCode != nil {
		builder.SetResponseCode(*rule.ResponseCode)
	}
	if rule.CustomMessage != nil {
		builder.SetCustomMessage(*rule.CustomMessage)
	}
	if rule.Description != nil {
		builder.SetDescription(*rule.Description)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(created), nil
}

// Update replaces a rule's fields, clearing omitted optional values.
func (r *errorPassthroughRepository) Update(ctx context.Context, rule *errorpolicy.ErrorPassthroughRule) (*errorpolicy.ErrorPassthroughRule, error) {
	builder := r.client.ErrorPassthroughRule.UpdateOneID(rule.ID).
		SetName(rule.Name).
		SetEnabled(rule.Enabled).
		SetPriority(rule.Priority).
		SetMatchMode(rule.MatchMode).
		SetPassthroughCode(rule.PassthroughCode).
		SetPassthroughBody(rule.PassthroughBody).
		SetSkipMonitoring(rule.SkipMonitoring)

	// Empty slices and nil pointers clear previously stored optional fields.
	if len(rule.ErrorCodes) > 0 {
		builder.SetErrorCodes(rule.ErrorCodes)
	} else {
		builder.ClearErrorCodes()
	}
	if len(rule.Keywords) > 0 {
		builder.SetKeywords(rule.Keywords)
	} else {
		builder.ClearKeywords()
	}
	if len(rule.Platforms) > 0 {
		builder.SetPlatforms(rule.Platforms)
	} else {
		builder.ClearPlatforms()
	}
	if rule.ResponseCode != nil {
		builder.SetResponseCode(*rule.ResponseCode)
	} else {
		builder.ClearResponseCode()
	}
	if rule.CustomMessage != nil {
		builder.SetCustomMessage(*rule.CustomMessage)
	} else {
		builder.ClearCustomMessage()
	}
	if rule.Description != nil {
		builder.SetDescription(*rule.Description)
	} else {
		builder.ClearDescription()
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(updated), nil
}

// Delete removes the rule with the given ID.
func (r *errorPassthroughRepository) Delete(ctx context.Context, id int64) error {
	return r.client.ErrorPassthroughRule.DeleteOneID(id).Exec(ctx)
}

// toModel converts an Ent entity to an error policy rule.
func (r *errorPassthroughRepository) toModel(e *ent.ErrorPassthroughRule) *errorpolicy.ErrorPassthroughRule {
	rule := &errorpolicy.ErrorPassthroughRule{
		ID:              e.ID,
		Name:            e.Name,
		Enabled:         e.Enabled,
		Priority:        e.Priority,
		ErrorCodes:      e.ErrorCodes,
		Keywords:        e.Keywords,
		MatchMode:       e.MatchMode,
		Platforms:       e.Platforms,
		PassthroughCode: e.PassthroughCode,
		PassthroughBody: e.PassthroughBody,
		SkipMonitoring:  e.SkipMonitoring,
		CreatedAt:       e.CreatedAt,
		UpdatedAt:       e.UpdatedAt,
	}

	if e.ResponseCode != nil {
		rule.ResponseCode = e.ResponseCode
	}
	if e.CustomMessage != nil {
		rule.CustomMessage = e.CustomMessage
	}
	if e.Description != nil {
		rule.Description = e.Description
	}

	// Represent empty collections as empty slices.
	if rule.ErrorCodes == nil {
		rule.ErrorCodes = []int{}
	}
	if rule.Keywords == nil {
		rule.Keywords = []string{}
	}
	if rule.Platforms == nil {
		rule.Platforms = []string{}
	}

	return rule
}
