// Package httpapi provides authentication, profile, and user-administration HTTP handlers and routes.
//
// Reading entry points:
//   - routes_auth.go: registration, login, session, and external authentication routes.
//   - session_handler.go: registration, password login, and session revocation.
//   - admin_user_handler.go: administrator user queries and updates.
//
// File groups:
//   - routes_auth.go, routes_admin.go, routes_settings.go, routes_user.go, auth_endpoints.go, authentication.go: route registration and authentication dispatch.
//   - OAuth files such as email_oauth.go and oauth_start.go, google_one_tap.go, pending_handler.go: external login, binding, and pending registration.
//   - session_handler.go, session_binding.go, logout_cookies.go, token_response.go: sessions, device information, and login responses.
//   - admin_auth.go, jwt_auth.go, step_up.go, backend_mode_guard.go: authentication, step-up verification, and backend-mode access.
//   - passkey_handler.go, totp_handler.go: Passkey and one-time password authentication.
//   - user_handler.go, admin_user_handler.go, user_attribute_handler.go, admin_key_settings.go, ports.go: profiles, administration, and handler dependencies.
package httpapi
