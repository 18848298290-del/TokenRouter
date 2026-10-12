// Package routing manages group policies, model catalogs, and pricing configurations and builds request route
// plans.
//
// Entry points:
//   - plan.go builds routes from the request group and protocol and resolves provider candidates.
//   - group_admin.go creates, updates, and deletes groups and queries available models.
//   - requestable.go resolves models from provider configuration, group mappings, and allowlists.
//
// File groups:
//   - group*.go and client_group.go handle group settings, administration, capacity, and availability probes.
//   - plan.go and reasoning.go handle model mapping, candidate protocols, and reasoning effort.
//   - model*.go and requestable*.go handle attributes, availability diagnostics, and requestable catalogs.
//   - pricing*.go handle price catalogs, configuration storage and caching, and time-based pricing.
//   - marketplace.go and availability_window.go provide public models, quotes, and availability windows.
//   - admin*.go and runtime_settings.go provide administrative catalogs and runtime routing settings.
package routing
