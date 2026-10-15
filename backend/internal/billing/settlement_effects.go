package billing

import (
	"context"
)

// settlement_effects.go reconciles billing caches and dispatches
// post-settlement callbacks.
// Committed funding results determine which cache entries need reconciliation
// after billing. A known balance below admission thresholds is invalidated for
// a later reload; other positive balance deductions are queued. Key-window
// increments use the applied subscription-plus-balance total when a result is
// supplied, with ActualCost available for callers without that result.
//
// Provider activity runs synchronously before configured notifications are
// submitted to the task runner. A missing runner executes each notification
// inline, and Finalize ignores its acceptance result when configured. These
// callbacks perform post-settlement work rather than own the durable
// transaction, so cache errors and omitted notification dispatch do not
// reverse a completed charge. Callers invoke this work for their appropriate
// applied result and keep asynchronous callback data usable.

// SettlementEffectInput pairs settlement amounts with caller-presence flags
// and cache record identities.
type SettlementEffectInput struct {
	// UserID, KeyID, HasUser and HasKeyRateLimits select the cache operations
	// together. Balance work requires a present payer; Key-window work requires
	// enabled limits, with IDs naming the corresponding cached records.
	// UserID: paying user record identifier.
	// KeyID: charged API Key identifier.
	// HasUser: caller supplied payer data.
	// HasKeyRateLimits: positive Key window ceilings.
	UserID, KeyID             int64
	HasUser, HasKeyRateLimits bool
	// Cost nil suppresses every Finalize effect. With a cost present, ActualCost
	// supplies Key-window usage only when Result is absent; supplied results take
	// precedence.
	Cost *CostBreakdown // priced request cost breakdown
	// Result supplies applied balance/subscription amounts and optional new
	// balance. Finalize does not inspect Applied or deduplicate callbacks; the
	// caller chooses when a result qualifies for post-commit work.
	Result *UsageBillingApplyResult // optional durable billing apply result
}

// SettlementEffects coordinates optional cache reconciliation, provider
// activity and notification callbacks after billing.
type SettlementEffects struct {
	// Cache supplies invalidation and queue methods. Nil cache skips balance
	// work, and the Eligibility queue method also tolerates a nil receiver for
	// Key-window work.
	Cache *Eligibility // optional eligibility cache writer
	// Background receives named notification callbacks and returns its runner
	// result. Finalize ignores that result; nil runs callbacks synchronously, so
	// callers account for either execution style.
	Background func(string, func()) bool // optional named notification task runner
	// Observe retains the supplied logging value, but these methods do not
	// consume it. Balance invalidation failures use BalanceWarning when that
	// callback is configured.
	Observe Observe // caller-provided billing observation value
	// BalanceWarning receives a failed invalidation with user ID and known new
	// balance. Without this callback Finalize still returns no error; Eligibility
	// can report through its optional observer.
	// optional balance invalidation error callback
	BalanceWarning func(int64, float64, error)
	// ProviderUsed runs synchronously when Cost is present, even for zero charge.
	// It precedes notification dispatch, and its panic is not recovered by this
	// adapter.
	ProviderUsed func() // optional synchronous provider activity callback
	// NotifyBalance and NotifyProvider are submitted after cache/provider steps
	// whenever configured, including zero-charge inputs. Callers keep captured
	// data usable for asynchronous execution; absent Background invokes them
	// inline.
	// NotifyBalance: optional payer notification callback.
	// NotifyProvider: optional provider notification callback.
	NotifyBalance, NotifyProvider func()
}

// Finalize reconciles caches and dispatches configured activity/notifications
// for a caller-selected settlement input. Nil Cost suppresses all work; a
// positive balance result with HasUser invokes SyncBalance using background
// context, and Result's funding total overrides ActualCost for positive
// Key-window increments.
//
// It does not test Applied or deduplicate this sequence, so the caller invokes
// it for the appropriate durable outcome. ProviderUsed runs synchronously
// before notification submissions, even when charged amounts are zero. A
// missing Background runs notifications inline; configured runner results are
// ignored, and cache reconciliation failures do not produce a returned
// settlement error.
func (e SettlementEffects) Finalize(input SettlementEffectInput) {
	if input.Cost == nil {
		return
	}
	if input.Result != nil && input.Result.BalanceAmountUSD > 0 && input.HasUser {
		// Reconcile against the committed result independently of request
		// cancellation; the supplied background context itself adds no deadline to
		// this invalidation path.
		e.SyncBalance(context.Background(), input.UserID, input.Result)
	}
	rateCost := input.Cost.ActualCost
	// Use the applied funding total for Key usage so subscription and balance
	// portions are counted once together, even when requested ActualCost differs.
	if input.Result != nil {
		rateCost = input.Result.SubscriptionAmountUSD + input.Result.BalanceAmountUSD
	}
	if rateCost > 0 && input.HasKeyRateLimits {
		e.Cache.QueueUpdateAPIKeyRateLimitUsage(input.KeyID, rateCost)
	}
	if e.ProviderUsed != nil {
		e.ProviderUsed()
	}
	if e.NotifyBalance != nil {
		e.launch("billing/settlement:notifyBalance", e.NotifyBalance)
	}
	if e.NotifyProvider != nil {
		e.launch("billing/settlement:notifyProvider", e.NotifyProvider)
	}
}

// launch submits a nonnil named callback to the configured runner and returns
// that runner's result. Without Background it executes synchronously and
// returns true; Finalize deliberately does not react to that result.
func (e SettlementEffects) launch(name string, fn func()) bool {
	if e.Background != nil {
		return e.Background(name, fn)
	}
	// Without a task runner the notification executes inline, so callers using
	// this default observe its latency or panic before Finalize can return.
	fn()
	return true
}

// SyncBalance reconciles a positive balance deduction using a known new
// balance when available. Nil cache/result or nonpositive deduction is a
// no-op. A known balance at or below zero, or strictly below a positive
// reserve, invalidates rather than decrements its cache entry.
//
// Invalidation uses the supplied context and reports failure through the
// optional warning callback, then returns without queueing deduction.
// Otherwise the deduction enters Eligibility's queue, including results with
// no new-balance observation. Queue admission and stopped-pool behavior remain
// Eligibility's responsibility; this method performs no durable transaction or
// returned-error handling.
func (e SettlementEffects) SyncBalance(ctx context.Context, id int64, result *UsageBillingApplyResult) {
	if e.Cache == nil || result == nil || result.BalanceAmountUSD <= 0 {
		return
	}
	// Invalidate an exhausted or under-reserve balance so a later precheck
	// reloads current storage rather than retaining a stale eligible cached
	// amount.
	if result.NewBalance != nil && e.Cache.BalanceBelowEligibilityThreshold(*result.NewBalance) {
		// Invalidation is best effort after durable billing. Report its error when
		// configured, but do not queue a deduction or replace the completed charge's
		// outcome.
		if err := e.Cache.InvalidateUserBalance(ctx, id); err != nil && e.BalanceWarning != nil {
			e.BalanceWarning(id, *result.NewBalance, err)
		}
		return
	}
	e.Cache.QueueDeductBalance(id, result.BalanceAmountUSD)
}

// BalanceBeforeSettlement reconstructs an arithmetic pre-deduction balance
// from a supplied new balance plus its deducted amount. Missing
// result/new-balance pointer uses the caller's snapshot; this helper validates
// neither Applied nor amount sign and mutates no input.
func BalanceBeforeSettlement(snapshot float64, result *UsageBillingApplyResult) float64 {
	if result != nil && result.NewBalance != nil {
		return *result.NewBalance + result.BalanceAmountUSD
	}
	// No stored post-deduction observation is available, so use the captured
	// snapshot rather than presenting an arithmetic balance reconstruction as a
	// database read.
	return snapshot
}
