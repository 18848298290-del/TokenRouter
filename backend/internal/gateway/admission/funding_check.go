package admission

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Waiting for request capacity can outlast a payer's available funds. Admission
// checks funding before counting RPM, then offers a funding-only recheck after
// the wait so the same request is not counted a second time.

// FundingCheck supplies payer, key, and subscription eligibility checks for admission.
type FundingCheck interface {
	// Check rejects requests whose supplied funding snapshot cannot cover admission.
	// The context belongs to the request, and callers run this check again after
	// waiting because funding eligibility can change during that wait.
	Check(context.Context, billing.CheckInput) error
}

// RPMCheck applies scheduler-owned request counts and group overrides.
type RPMCheck interface {
	// Check admits a request using the user's and optional group's RPM settings.
	// A successful call consumes request capacity, so FundingAdmission invokes it
	// after funding succeeds and skips it during post-wait rechecks.
	Check(context.Context, *scheduler.RPMUser, *scheduler.RPMGroup) error
}

// FundingAdmission checks funds before consuming initial request RPM capacity.
type FundingAdmission struct {
	// funds is required for initial admission and post-wait checks. The application
	// supplies the billing eligibility service when constructing this checker.
	funds FundingCheck
	// rpm counts initially admitted requests when configured and payer data exists.
	// A nil checker permits funding-only admission without a request-counting call.
	rpm RPMCheck
}

// NewFundingAdmission binds a required funding checker and an optional RPM
// checker for gateway admission. Construction performs no checks; a nil RPM
// checker leaves request-count admission disabled.
func NewFundingAdmission(funds FundingCheck, rpm RPMCheck) *FundingAdmission {
	return &FundingAdmission{funds: funds, rpm: rpm}
}

// Check validates funding and then consumes the request's RPM capacity.
// Funding errors return before RPM accounting; a nil RPM checker or user skips
// counting. Use CheckFunding for a request already counted before waiting.
func (a *FundingAdmission) Check(ctx context.Context, input billing.CheckInput, user *scheduler.RPMUser, group *scheduler.RPMGroup) error {
	if err := a.CheckFunding(ctx, input); err != nil {
		return err
	}
	if a.rpm == nil || user == nil {
		return nil
	}
	return a.rpm.Check(ctx, user, group)
}

// CheckFunding validates the supplied funding snapshot without counting another
// RPM request. Gateway callers use it after obtaining a waited-for slot, because
// balance or subscription eligibility may have changed during the wait.
func (a *FundingAdmission) CheckFunding(ctx context.Context, input billing.CheckInput) error {
	return a.funds.Check(ctx, input)
}

// CheckKey builds billing and RPM inputs from an authenticated key whose final
// group is already authorized. Supply a nonnil key and the resolved subscription;
// afterWait selects a funding-only recheck for a request counted before waiting.
func (a *FundingAdmission) CheckKey(ctx context.Context, key *apikey.APIKey, subscription *billing.UserSubscription, platform string, afterWait bool) error {
	var payer *billing.UserSummary
	var user *scheduler.RPMUser
	if key.User != nil {
		payer = &billing.UserSummary{ID: key.User.ID}
		user = &scheduler.RPMUser{ID: key.User.ID, RPMLimit: key.User.RPMLimit, UserGroupRPMOverride: key.User.UserGroupRPMOverride}
	}
	var group *billing.GroupSnapshot
	var rpmGroup *scheduler.RPMGroup
	if key.Group != nil {
		group = &billing.GroupSnapshot{ID: key.Group.ID}
		rpmGroup = &scheduler.RPMGroup{ID: key.Group.ID, RPMLimit: key.Group.RPMLimit}
	}
	input := billing.CheckInput{
		Payer: payer,
		Key:   &billing.KeySnapshot{ID: key.ID, BillingMode: key.BillingMode, RateLimit5h: key.RateLimit5h, RateLimit1d: key.RateLimit1d, RateLimit7d: key.RateLimit7d},
		Group: group, Subscription: subscription, Platform: platform,
	}
	if afterWait {
		return a.CheckFunding(ctx, input)
	}
	return a.Check(ctx, input, user, rpmGroup)
}
