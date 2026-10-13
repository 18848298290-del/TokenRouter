// Package routing keeps group model permissions and client admission separate
// from provider execution and shared price configuration. It resolves saved
// candidates into request routes and public model views, while administration
// persists group settings, memberships, and reusable billing rules through
// injected storage.
//
// Start with plan.go for request routing, group_admin.go for group mutations,
// and requestable.go for finite configured model catalogs. group*.go and
// client_group.go cover policy, capacity, and probe lifetimes; model*.go and
// requestable*.go cover attributes and eligibility. pricing*.go cover shared
// settings, card lookup, and cache publication. marketplace.go and
// availability_window.go assemble public quotes and probe windows, and
// admin*.go plus runtime_settings.go handle administrative catalogs and stored
// routing settings.
package routing
