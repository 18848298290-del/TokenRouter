// Package postgres persists billing balances, plans, subscriptions and
// redemption codes, and settles request and task funds. Settlement owns its
// complete SQL transaction, while explicit Ent participants join caller-owned
// entitlement transactions with their supplied connection.
//
// Reading entry points:
//   - settlement.go: charges requests and reserves, captures, and releases task funds.
//   - plans.go: reads plans and updates plan/group mappings in transactions.
//   - user_subscription_repo.go: queries subscriptions and writes status, validity, and quota windows.
//
// File groups:
//   - balance.go, available_balance.go: update balances, initialize funds, and deduct available balances.
//   - settlement.go, usage_dedup_retention.go: settle funds, coordinate task writes, and archive deduplication records.
//   - key_usage.go, member_usage.go: accumulate API Key and member usage and reset windows.
//   - provider_usage.go, provider_usage_store.go, provider_quota_expressions.go: write provider usage, handle
//     quota periods, and notify changes.
//   - plans.go, user_subscription_repo.go, subscription_transactions.go, user_group_rate_repo.go: persist plans,
//     subscriptions, group rates, and entitlement transactions.
//   - redeem_code_repo.go, redeem_administration.go, redeem_transactions.go, redeem_writers.go,
//     redeem_update_builder.go: persist redemption codes and write entitlements.
//   - participants.go, registration_invitation.go: participate in caller-owned balance, subscription, redemption,
//     and registration transactions.
//   - entity_helpers.go: select transaction clients, map database errors, and convert entities.
package postgres
