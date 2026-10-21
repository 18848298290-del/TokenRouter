import type { User } from '@/api/identity.types'

// auth.types.ts defines the credentials and verification proofs exchanged during
// registration and login. Authentication responses include the user profile, while
// optional refresh-token and expiry fields allow callers to handle responses that provide
// different token-renewal information.

// LoginRequest supplies account credentials and optional human-verification proof for
// authentication.
export interface LoginRequest {
  email: string // Account email address.
  password: string // Account authentication password.
  turnstile_token?: string // Human-verification challenge proof.
  tencent_captcha_ticket?: string // Tencent verification ticket.
  tencent_captcha_randstr?: string // Tencent verification random string.
}

// TencentCaptchaRequestProof supplies the ticket and random string returned by Tencent
// verification.
export interface TencentCaptchaRequestProof {
  tencent_captcha_ticket: string // Tencent verification ticket.
  tencent_captcha_randstr: string // Tencent verification random string.
}

// ActionCaptchaRequestProof carries verification credentials for OAuth startup and
// passkey actions. Tencent uses tencent_captcha_*; Aliyun sends captchaVerifyParam
// through turnstile_token for the same request interface.
export interface ActionCaptchaRequestProof extends Partial<TencentCaptchaRequestProof> {
  turnstile_token?: string // Human-verification challenge proof.
}

// RegisterRequest supplies account details and optional invitation or promotion
// credentials.
export interface RegisterRequest {
  email: string // Account email address.
  password: string // Account authentication password.
  verify_code?: string // Email verification code.
  turnstile_token?: string // Human-verification challenge proof.
  tencent_captcha_ticket?: string // Tencent verification ticket.
  tencent_captcha_randstr?: string // Tencent verification random string.
  promo_code?: string // Registration promotion code.
  invitation_code?: string // Account invitation code.
  aff_code?: string // Referral attribution code.
}

// SendVerifyCodeRequest selects an email destination and supplies optional verification
// context.
export interface SendVerifyCodeRequest {
  email: string // Account email address.
  turnstile_token?: string // Human-verification challenge proof.
  tencent_captcha_ticket?: string // Tencent verification ticket.
  tencent_captcha_randstr?: string // Tencent verification random string.
  pending_auth_token?: string // Pending authentication token.
  pending_oauth_token?: string // Pending OAuth verification token.
}

// SendVerifyCodeResponse reports email-code dispatch and the remaining resend delay.
export interface SendVerifyCodeResponse {
  message: string // Human-readable result message.
  countdown: number // Email resend cooldown seconds.
}

// AuthResponse returns authentication tokens together with the authenticated account
// profile.
export interface AuthResponse {
  access_token: string // Session access token.
  refresh_token?: string // Session renewal token.
  expires_in?: number // Access token validity seconds.
  token_type: string // Authentication token scheme.
  user: User // Expanded account details.
}

// CurrentUserResponse is the account profile returned for the current session.
export type CurrentUserResponse = User
