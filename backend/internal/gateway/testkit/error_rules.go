package testkit

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
)

// Error-presentation tests can use the production rule matcher with an
// in-memory rule list. Starting that service publishes its compiled snapshot,
// while the non-failover fixture supplies an all-category match and fixed
// response settings for callers testing final error formatting.

// errorRulesFixtureRepo supplies a preset list to the production rule service.
type errorRulesFixtureRepo struct {
	errorpolicy.ErrorPassthroughRepository                                     // unused inherited repository operations
	rules                                  []*errorpolicy.ErrorPassthroughRule // caller-supplied rule fixture pointers
}

// List returns the supplied rule pointers for the service's snapshot
// compilation.
func (r errorRulesFixtureRepo) List(context.Context) ([]*errorpolicy.ErrorPassthroughRule, error) {
	return r.rules, nil
}

// ErrorRules starts a production matcher using the supplied list and no shared
// cache. Startup errors panic; callers stop the returned service when their
// test finishes to release its lifecycle state.
func ErrorRules(rules []*errorpolicy.ErrorPassthroughRule) *errorpolicy.ErrorPassthroughService {
	s := errorpolicy.NewErrorPassthroughService(errorRulesFixtureRepo{rules: rules}, nil, gatewaytelemetry.ErrorRules)
	if err := s.StartContext(context.Background()); err != nil {
		panic(err)
	}
	return s
}

// NonFailoverRule creates an enabled rule requiring both the supplied status
// and keyword. Its response uses the custom code and message; the fixture
// configures final presentation and delegates retry classification to the
// caller.
func NonFailoverRule(statusCode int, keyword string, respCode int, customMessage string) *errorpolicy.ErrorPassthroughRule {
	return &errorpolicy.ErrorPassthroughRule{
		ID:              1,
		Name:            "non-failover-rule",
		Enabled:         true,
		Priority:        1,
		ErrorCodes:      []int{statusCode},
		Keywords:        []string{keyword},
		MatchMode:       errorpolicy.MatchModeAll,
		PassthroughCode: false,
		ResponseCode:    &respCode,
		PassthroughBody: false,
		CustomMessage:   &customMessage,
	}
}
