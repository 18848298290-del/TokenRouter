// Package identity manages users, registration, login, sessions, and authentication settings.
//
// Reading entry points:
//   - auth_service.go: registration, login, CAPTCHA, and password reset.
//   - user_service.go: profiles, identity binding, and notification email.
//   - session_service.go: access tokens, refresh tokens, and session revocation.
//
// File groups:
//   - admin_*.go: user administration, transactions, and authentication settings.
//   - auth_*.go, bootstrap.go: authentication dependencies, email binding, OAuth signup, and initial administrator creation.
//   - user*.go, risk_status.go: user data, profile updates, custom attributes, and risk status.
//   - session_*.go, refresh_token_cache.go, clock.go: session tokens, cache interfaces, and clock access.
//   - pending*.go, oauth_*.go: pending authentication, external identities, and authorization rules.
//   - dingtalk_*.go, wechat_oauth.go, linuxdo_oauth.go, oidc_oauth.go, email_oauth.go: provider-specific OAuth data and identity handling.
//   - email_*.go, registration_email_*.go: email challenges and registration email rules.
//   - captcha.go, aliyun_captcha_service.go, tencent_captcha_service.go, turnstile_service.go, totp_service.go, passkey.go: CAPTCHA, two-factor authentication, and passkeys.
//   - runtime_settings.go, public_settings.go, settings_participant.go, grant_settings*.go: authentication switches, public settings, and signup grants.
package identity
