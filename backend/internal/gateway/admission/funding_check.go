package admission

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// FundingCheck validates funding availability.
type FundingCheck interface {
	Check(context.Context, billing.CheckInput) error
}

// RPMCheck applies scheduler-owned request counts and group overrides.
type RPMCheck interface {
	Check(context.Context, *scheduler.RPMUser, *scheduler.RPMGroup) error
}

// FundingAdmission checks funds before consuming RPM capacity.
type FundingAdmission struct {
	funds FundingCheck
	rpm   RPMCheck
}

// NewFundingAdmission binds funding and RPM checks.
func NewFundingAdmission(funds FundingCheck, rpm RPMCheck) *FundingAdmission {
	return &FundingAdmission{funds: funds, rpm: rpm}
}

// Check consumes RPM capacity after funding admission succeeds.
func (a *FundingAdmission) Check(ctx context.Context, input billing.CheckInput, user *scheduler.RPMUser, group *scheduler.RPMGroup) error {
	if err := a.CheckFunding(ctx, input); err != nil {
		return err
	}
	if a.rpm == nil || user == nil {
		return nil
	}
	return a.rpm.Check(ctx, user, group)
}

// CheckFunding rechecks funding after a wait; RPM was already counted before waiting.
func (a *FundingAdmission) CheckFunding(ctx context.Context, input billing.CheckInput) error {
	return a.funds.Check(ctx, input)
}

// CheckKey uses an authenticated key whose final group has been authorized. After a wait it checks funding alone.
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
