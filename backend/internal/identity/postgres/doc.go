// Package postgres persists users, authentication identities, pending sessions, and group grants.
//
// Reading entry points:
//   - user_repo.go: user profiles, email uniqueness, group grants, and balance operations.
//   - auth_state.go: identity writes during registration and login.
//   - pending.go: pending sessions, completion codes, and profile adoption choices.
//
// File groups:
//   - user_*.go: user repositories, identity bindings, attributes, and entity conversion.
//   - auth_*.go and oauth_lookup.go: registration, login, and OAuth identity lookup.
//   - pending*.go: pending sessions, identity binding, and profile adoption.
//   - admin_mutations.go and bootstrap_admin.go: bootstrap and administrative user operations.
//   - group_access_*.go: grant changes within caller-owned transactions.
//   - passkey.go: credential storage, lookup, and updates.
//   - concurrency.go and risk_status.go: concurrency limits and risk-based disabling.
package postgres
