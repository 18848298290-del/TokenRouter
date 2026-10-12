package completion

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Committed balance and quota amounts supply notification evidence after funding
// is applied. Billing's Finalize orders cache updates and activity before dispatching
// notification callbacks, while each notification check recovers its own panic so
// an alert failure does not alter the completed financial result.

type (
	// ProviderActivity schedules provider last-use persistence after observed usage.
	ProviderActivity interface {
		// ScheduleLastUsedUpdate records activity for the supplied executing provider.
		// Completion delegates batching and persistence to the activity service, so
		// this call does not establish when the database timestamp becomes visible.
		ScheduleLastUsedUpdate(int64)
	}
	// AuthInvalidator expires credential admission after committed key quota changes.
	AuthInvalidator interface {
		// InvalidateAuthCacheByKey expires the supplied credential's cached admission.
		// Completion invokes it after captured quota updates exhaust a key and before
		// dispatching the transaction's remaining cache and notification effects.
		InvalidateAuthCacheByKey(context.Context, string)
	}
)

// CommitEffects binds cache, activity, and threshold-check callbacks after
// settlement commits, with dispatch order delegated to billing.Finalize.
type CommitEffects struct {
	// Funds is copied before request callbacks are installed. Its Cache is required
	// for positive key-window updates; Background controls notification task admission.
	Funds billing.SettlementEffects // shared funds cache callbacks
	// Activity is required before ProviderUsed or Settled schedules last-use writes.
	// The bound service owns batching, failure handling, and concurrent access.
	Activity ProviderActivity // provider last-use scheduler
	// Auth is optional; absent invalidation skips exhausted-key cache expiry
	// while the remaining committed effects continue through billing.Finalize.
	Auth AuthInvalidator // optional credential cache invalidator
	// Notifications enables post-settlement threshold checks; nil skips both
	// balance and provider alerts while cache and provider activity work continues.
	Notifications *billing.BalanceNotifyService // optional threshold notification service
	// Observe receives recovered alert panics when configured; an absent callback
	// still lets the deferred handler consume those notification failures.
	Observe func(string, string) // recovered notification panic observer
}

// ProviderUsed schedules last-use persistence through the required activity
// service for the executing provider whose usage was observed. Activity must be
// bound before use; its service owns batching and persistence error handling.
func (e *CommitEffects) ProviderUsed(id int64) { e.Activity.ScheduleLastUsedUpdate(id) }

// InvalidateAuth delegates exhausted-key admission invalidation when configured.
// A nil Auth binding skips this optional effect. The caller passes the captured
// credential value and the detached settlement context.
func (e *CommitEffects) InvalidateAuth(ctx context.Context, key string) {
	if e.Auth != nil {
		e.Auth.InvalidateAuthCacheByKey(ctx, key)
	}
}

// Settled binds provider activity and threshold callbacks to billing's Finalize.
// It copies Funds by value, then supplies payer and key metadata with the committed
// result. Callers provide valid provider and cost data and the required cache services
// for any funded effects; background notification admission belongs to Funds.
func (e *CommitEffects) Settled(p SettlementInput, result *billing.UsageBillingApplyResult) {
	// Per-settlement closures capture this request's identities while the copied
	// Funds value retains application-owned cache and background scheduling services.
	effects := e.Funds
	effects.ProviderUsed = func() { e.ProviderUsed(p.Provider.ID) }
	effects.NotifyBalance = func() { e.NotifyBalance(p, result) }
	effects.NotifyProvider = func() { e.NotifyProvider(p, result) }
	in := billing.SettlementEffectInput{Cost: p.Cost, Result: result, HasUser: p.User != nil}
	if p.User != nil {
		in.UserID = p.User.ID
	}
	if p.APIKey != nil {
		in.KeyID = p.APIKey.ID
		in.HasKeyRateLimits = p.APIKey.HasRateLimits
	}
	effects.Finalize(in)
}

// recoverNotification consumes a deferred notification panic and optionally
// reports its value. Call it directly with defer from the notification method;
// a nil observer still consumes the panic, and observer failures propagate.
func (e *CommitEffects) recoverNotification(name string) {
	// Alert checks run after committed funding; consume their panics locally so
	// an alert failure cannot turn completed settlement into a task panic.
	if v := recover(); v != nil && e.Observe != nil {
		e.Observe(name, fmt.Sprint(v))
	}
}

// NotifyBalance checks a positive committed balance deduction against captured
// payer notification settings. Missing result, payer, or notification service skips
// the check. It reconstructs the previous balance from NewBalance when available,
// and its deferred handler consumes notification panics.
func (e *CommitEffects) NotifyBalance(p SettlementInput, result *billing.UsageBillingApplyResult) {
	defer e.recoverNotification("notifyBalanceLow")
	if result == nil || result.BalanceAmountUSD <= 0 || p.User == nil || e.Notifications == nil {
		return
	}
	e.Notifications.CheckBalanceAfterDeduction(context.Background(), p.User.Notification, billing.BalanceBeforeSettlement(p.User.Balance, result), result.BalanceAmountUSD)
}

// NotifyProvider checks quota thresholds for eligible positive model cost.
// Missing provider, eligibility, notification service, or positive TotalCost skips
// notification. Committed QuotaState is supplied when available; the increment
// estimate uses TotalCost times provider rate, and notification panics are consumed.
// Cost must be nonnil when callers invoke this notification helper.
func (e *CommitEffects) NotifyProvider(p SettlementInput, result *billing.UsageBillingApplyResult) {
	defer e.recoverNotification("notifyProviderQuota")
	if p.Cost.TotalCost <= 0 || p.Provider == nil || !p.Provider.QuotaEligible || e.Notifications == nil {
		return
	}
	var state *billing.ProviderQuotaState
	if result != nil {
		state = result.QuotaState
	}
	e.Notifications.CheckProviderQuotaAfterIncrement(context.Background(), p.Provider.Notification, p.Cost.TotalCost*p.ProviderRateMultiplier, state)
}
