// Package httpapi handles provider administration, authorization, usage queries, and connection tests over HTTP.
//
// Reading entry points:
//   - routes_admin.go registers administrator and platform authorization endpoints.
//   - management.go defines management dependencies and handlers.
//   - test_handler.go runs connection tests and streams their events.
//
// File groups:
//   - management*.go, archive.go, runtime_presenter.go manage providers, transfer data, and build status responses.
//   - openai_oauth.go, claude_oauth.go, gemini_oauth.go, antigravity_oauth.go, grok_oauth.go, qoder_oauth.go handle platform authorization.
//   - codex_import.go, codex_invite_reset.go, crs.go handle Codex credentials, invitations, and CRS operations.
//   - oauth_usage.go, ollama_usage.go, upstream_usage.go query account and upstream usage.
//   - runtime_settings.go, routes_settings.go, routes_admin.go expose runtime settings and register routes.
//   - test_handler.go, test_events.go, scheduled_plans.go run connection tests, emit events, and manage test schedules.
package httpapi
