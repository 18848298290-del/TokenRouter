// Package grok adapts xAI authentication, requests, streaming responses, and media APIs.
//
// Start with:
//   - request_patch.go: Responses model, tool, and history normalization.
//   - responses_execute.go: Responses execution, usage, and completion state.
//   - oauth.go: OAuth settings and authorization and API URLs.
//
// File groups:
//   - oauth*.go and sso_device.go: authorization, token exchange, and device login.
//   - request_patch.go, model_input.go, and compact.go: request fields, tool history, and compaction.
//   - cache*.go, chat*.go, and composer_images.go: cache identities, chat conversion, and image descriptions.
//   - responses*.go and sse_filter.go: Responses requests, retries, and stream filtering.
//   - media*.go, video_content.go, voice*.go, and realtime*.go: images, video, audio, and realtime connections.
//   - billing*.go, quota*.go, subscription_tier.go, and audio_usage.go: billing, quotas, tiers, and audio usage.
//   - models.go, observed_models.go, standalone_search.go, and search_count.go: models and search usage.
//   - cli_identity.go, endpoint_policy.go, and transport_fallback.go: client identity, targets, and transport retries.
//   - errors.go and failure_classifier.go: error recognition, cooldowns, and failover conditions.
package grok
