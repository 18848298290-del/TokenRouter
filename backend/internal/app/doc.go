// Package app constructs TokenRouter services, connects HTTP handlers, stores, and workers, and registers
// lifecycle order.
//
// Entry points:
//   - application.go initializes the application, starts services, and waits for resource shutdown.
//   - wire.go declares the complete application dependency graph.
//   - http_routes.go combines authentication, user, gateway, and payment routes.
//
// File groups:
//   - assembly_*.go registers module constructors and interface bindings for Wire.
//   - gateway_*.go and qoder_*.go bind request admission, execution, response output, and completion recording.
//   - provider_*.go binds provider management, credential refresh, health, and usage queries.
//   - identity_*.go, apikey*.go, and team.go bind identity, API key, and team services.
//   - billing.go, payment_*.go, and routing_*.go bind funds, payments, groups, and pricing.
//   - tasks.go, task_storage.go, creative_*.go, and batch_image_*.go bind persistent tasks and image generation.
//   - settings_*.go, site_public.go, and http_*.go bind runtime settings, public site data, and HTTP handlers.
//   - storage.go, scheduler*.go, and runtime*.go construct shared resources and register background service
//
// lifecycles.
//   - ops*.go, observability_foundation.go, usage*.go, and notification.go bind monitoring, audit, usage, and
//
// notifications.
package app
