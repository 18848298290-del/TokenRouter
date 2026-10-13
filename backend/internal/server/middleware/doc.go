// Package middleware applies HTTP correlation, logging, browser policy, and
// ingress admission rules around Gin handlers. Global request capture runs
// before route handling, while gateway middleware reuses its identity and
// rejection markers feed aggregate observation after admission finishes.
//
// Start with:
//   - client_request_id.go: internal request IDs and caller correlation IDs.
//   - logger.go: access logs and ingress rejection results.
//   - security_headers.go: CSP nonces and browser security headers.
//
// File groups:
//   - client_request_id.go, request_logger.go, server_timing.go: request IDs, logging context, and response timing.
//   - logger.go, audit_log.go, ingress_reject*.go: access logs, audit actions, and ingress rejection records.
//   - rate_limiter.go, panel_rate_limit.go: API and panel rate limits.
//   - cors.go, security_headers.go, request_body_limit.go: cross-origin policy, security headers, and body limits.
//   - locale.go, provider_terminology.go, recovery.go: language selection, admin-field validation, and panic recovery.
package middleware
