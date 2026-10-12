// Package billing calculates request costs and manages balances,
// subscriptions, and redemption codes. Gateway callers check funding through
// Eligibility and resolve prices before Funds settles the selected
// subscription and balance allocations through persistence adapters.
//
// Reading entry points:
//   - calculator.go: calculates costs from model price cards and request usage.
//   - settlement.go: defines request settlement and task fund reservation, capture, and release data.
//   - subscription_service.go: assigns, extends, and revokes subscriptions and maintains quota windows.
//
// File groups:
//   - calculator.go, price_resolver.go, default_catalog.go: resolve configured and catalog prices and calculate
//     charges.
//   - public_quote.go, media_pricing.go: build public quotes and resolve image unit prices.
//   - settlement*.go, funds.go, allocation.go: allocate subscription and balance charges, settle usage, and update
//     caches.
//   - eligibility*.go, cache.go, key_rate_limits.go, member_quota.go: check balances, API Key limits, and member
//     quotas.
//   - subscription*.go, plan*.go, clock.go: manage plans, subscription states, and date windows.
//   - redeem_*.go, balance.go, user_summary.go: manage redemption codes and user data for balance adjustments.
//   - group_rate_*.go, provider_*.go, window_cost_*.go: manage user group multipliers and provider cost windows.
//   - balance_notifications.go, notification_thresholds.go: determine balance and quota alert conditions.
//   - admin_settings*.go, default_settings.go, settings_participant.go: read and validate billing settings.
package billing
