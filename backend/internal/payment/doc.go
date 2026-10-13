// Package payment manages payment settings, orders, provider selection, confirmation, fulfillment, and refunds.
//
// Entry points:
//   - runtime.go exposes the checkout, query, refund, lifecycle, and binding instances assembled by app.
//   - checkout.go validates requests, calls providers, and builds checkout responses.
//   - fulfillment.go validates notifications, confirms payment, and grants balance or subscriptions.
//
// File groups:
//   - configuration*.go and settings_participant.go read settings and manage provider instances.
//   - visible_*.go and method_localization.go select visible methods and localize their names.
//   - checkout*.go and billing_info.go validate checkout, buyer details, and order snapshots.
//   - order*.go, query_ports.go, query_types.go, document.go, and statistics.go handle order state and queries.
//   - fulfillment*.go and expiry.go confirm payments, grant rights, and process expired orders.
//   - refund_*.go validate refunds, call providers, and deduct or restore rights.
//   - provider_*.go, registry.go, load_balancer.go, and instance.go register, bind, and select providers.
//   - amount_rules.go, currency.go, and fee.go calculate recharge amounts, currency precision, and fees.
//   - resume*.go and crypto.go sign resume tokens and read encrypted configuration.
//   - types.go and management_ports.go define payment requests and provider and management interfaces.
package payment
