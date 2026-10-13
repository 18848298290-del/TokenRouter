// Package identity maps local password credentials and external provider
// tuples to current accounts. Account email and password data determine
// credential fingerprints, while OAuth pending sessions keep browser proof
// separate from provider claims and account-choice state.
//
// Registration and binding can write account, identity, invitation, and grant
// state in different persistence scopes. Core services select accounts and
// profile choices, storage adapters perform the writes, and HTTP handlers
// control browser cookies and response order. An earlier credential or
// completed write can survive a later completion error, so callers follow each
// workflow's transaction and compensation rules.
//
// Begin with auth_service.go for registration, password login, CAPTCHA
// admission, and reset requests. user_service.go handles profiles, identity
// binding, and notification recipients. session_service.go implements access
// and refresh credentials, rotation, and revocation. pending_flow.go explains
// account finalization and its compensation, while pending_rules.go and
// pending_decisions.go select browser continuation and adoption behavior.
//
// File groups:
//   - admin_*.go covers user administration and its transaction and settings
//     dependencies.
//   - auth_*.go contains authentication workflows, options, dependencies, and
//     persistence participation. bootstrap.go models initial administrator
//     setup.
//   - user*.go and risk_status.go contain account values, repository
//     interfaces, profile operations, custom attributes, and risk-status
//     participation.
//   - session_*.go, refresh_token_cache.go, and clock.go define credential
//     lifecycle, request binding, cache operations, and clock observations.
//   - pending*.go and oauth_*.go describe browser state, account choice,
//     provider subjects, and profile-adoption rules.
//   - dingtalk_*.go, wechat_oauth.go, linuxdo_oauth.go, oidc_oauth.go, and
//     email_oauth.go contain provider-specific identity data and login rules.
//   - email_*.go and registration_email_*.go handle challenge state, email
//     normalization, and registration policy.
//   - captcha.go, aliyun_captcha_service.go, tencent_captcha_service.go,
//     turnstile_service.go, totp_service.go, and passkey.go define CAPTCHA and
//     strong-factor operations.
//   - runtime_settings.go, public_settings.go, settings_participant.go, and
//     grant_settings*.go supply runtime authentication decisions and signup or
//     binding grants.
package identity
