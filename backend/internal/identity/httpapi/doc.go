// Package httpapi serves panel authentication, current-account management, and
// administrator user operations. Route registration combines caller-supplied
// guards with shared handlers; OAuth continuations use pending browser state,
// while JWT middleware reloads the current account before exposing identity.
//
// Begin with routes_auth.go for login and external authentication routes,
// session_handler.go for password and session operations, and
// admin_user_handler.go for administrator account queries and updates.
//
// The routes_*.go group registers endpoint families. auth_endpoints.go and
// authentication.go describe and dispatch their shared authentication
// operations. Provider OAuth files, oauth_start.go, google_one_tap.go, and
// pending_handler.go handle external proof and browser continuations.
//
// Session handling, session_binding.go, logout_cookies.go, and
// token_response.go cover credentials, request fingerprints, cleanup, and
// responses. admin_auth.go, jwt_auth.go, step_up.go, and backend_mode_guard.go
// apply credential and backend access checks. Passkey and TOTP handlers
// implement strong-authentication HTTP operations.
//
// User and administrator handlers, user_attribute_handler.go, and
// admin_key_settings.go serve profiles, attributes, and management-key
// configuration. ports.go names the caller interfaces used by authentication
// middleware.
package httpapi
