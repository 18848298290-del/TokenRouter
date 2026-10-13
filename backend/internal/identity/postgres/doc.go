// Package postgres stores accounts, provider identities, browser pending
// state, and account group grants. Its adapters join caller transactions where
// their operations support participation, while account and pending workflows
// own the required ordering, rollback, and stored-row checks before
// completion.
//
// Begin with user_repo.go for accounts, email uniqueness, grants, and balance
// operations, auth_state.go for registration and login writes, and pending.go
// for browser sessions, completion codes, and adoption choices.
//
// The user_*.go group contains profile and attribute repositories, identity
// bindings, and entity conversion. auth_*.go and oauth_lookup.go support
// registration, login, and provider lookup. pending*.go implements session
// state, binding, and profile adoption.
//
// admin_mutations.go and bootstrap_admin.go implement administrative account
// writes and initial administrator creation. group_access_*.go participates in
// caller transactions for grant changes. passkey.go stores WebAuthn
// credentials; concurrency.go and risk_status.go write allowance and
// account-disable state.
package postgres
