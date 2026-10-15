package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/errorpassthroughrule"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
)

// error_passthrough_repo.go stores and retrieves error passthrough rules
// through Ent and applies complete rule replacements.
// Stored error rules include match conditions, response actions, and ordering
// metadata in one Ent record. Creating omits absent optional values, while
// replacement updates clear omitted values so an earlier keyword, platform,
// status, or message cannot remain active merely because the new input leaves
// it empty.

// errorPassthroughRepository stores complete error-response rules through the
// shared Ent client.
type errorPassthroughRepository struct {
	client *ent.Client // shared application Ent database client
}

// NewErrorPassthroughRepository binds the supplied Ent client for error-rule
// persistence.
func NewErrorPassthroughRepository(client *ent.Client) errorpolicy.ErrorPassthroughRepository {
	return &errorPassthroughRepository{client: client}
}

// List returns all stored rules in ascending priority order after model
// conversion. Equal priorities have no additional ordering key here, and query
// errors return before any result collection is produced.
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

// GetByID returns the stored rule after model conversion or nil on Ent
// not-found. Other database errors propagate; callers use the nil result to
// identify an absent rule.
func (r *errorPassthroughRepository) GetByID(ctx context.Context, id int64) (*errorpolicy.ErrorPassthroughRule, error) {
	rule, err := r.client.ErrorPassthroughRule.Get(ctx, id)
	if err != nil {
		// Represent an absent rule as a successful nil lookup so administration can
		// select its not-found response; other database errors still propagate as
		// storage failures.
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toModel(rule), nil
}

// Create saves a nonnil rule with its required fields and supplied optional
// values. The caller performs domain validation; absent optional slices or
// pointers use the Ent creation defaults.
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

// Update replaces all fields of the nonnil rule identified by ID in one Ent
// save. Empty optional collections and nil pointers clear stored values, while
// the caller owns prior domain validation.
func (r *errorPassthroughRepository) Update(ctx context.Context, rule *errorpolicy.ErrorPassthroughRule) (*errorpolicy.ErrorPassthroughRule, error) {
	builder := r.client.ErrorPassthroughRule.UpdateOneID(rule.ID).
		SetName(rule.Name).
		SetEnabled(rule.Enabled).
		SetPriority(rule.Priority).
		SetMatchMode(rule.MatchMode).
		SetPassthroughCode(rule.PassthroughCode).
		SetPassthroughBody(rule.PassthroughBody).
		SetSkipMonitoring(rule.SkipMonitoring)

	// Clear omitted optional values in the same update as match and response
	// fields, so replacement does not keep an earlier rule condition or custom
	// action value.
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

// Delete removes the rule identified by ID through the Ent delete operation.
// Database errors, including missing-record handling from that operation,
// propagate directly to the service caller.
func (r *errorPassthroughRepository) Delete(ctx context.Context, id int64) error {
	return r.client.ErrorPassthroughRule.DeleteOneID(id).Exec(ctx)
}

// toModel converts a usable Ent rule and normalizes nil collections to nonnil
// empty slices. Nonnil slices and optional pointers retain entity storage; the
// service later clones mutable rule data for publication and results.
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

	// Represent absent collections as JSON-ready empty slices for rule consumers;
	// nonempty entity slices remain shared until the service performs its
	// separate rule cloning.
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
