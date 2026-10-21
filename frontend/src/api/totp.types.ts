// totp.types.ts defines authenticator enrollment, removal, and additional login
// verification messages. Setup returns temporary secret material and a confirmation
// token, while a login requiring another factor uses its temporary login token together
// with the generated authenticator code.

// TotpStatus reports account enrollment and site availability for authenticator
// verification.
export interface TotpStatus {
  enabled: boolean // Feature activation switch.
  enabled_at: number | null // Enrollment time in Unix seconds.
  feature_enabled: boolean // Site authenticator availability.
}

// TotpSetupRequest verifies account ownership before preparing an authenticator
// enrollment secret.
export interface TotpSetupRequest {
  email_code?: string // Email ownership proof.
  password?: string // Account authentication password.
}

// TotpSetupResponse supplies enrollment secret material and a temporary confirmation
// token.
export interface TotpSetupResponse {
  secret: string // Authenticator enrollment secret.
  qr_code_url: string // Authenticator enrollment QR URL.
  setup_token: string // Temporary enrollment confirmation token.
  countdown: number // Enrollment token lifetime seconds.
}

// TotpEnableRequest confirms enrollment using an authenticator code and the setup token.
export interface TotpEnableRequest {
  totp_code: string // Generated authenticator code.
  setup_token: string // Temporary enrollment confirmation token.
}

// TotpEnableResponse reports whether the authenticator enrollment confirmation completed
// successfully.
export interface TotpEnableResponse {
  success: boolean // Operation completion flag.
}

// TotpDisableRequest supplies account ownership proof for removing authenticator
// verification.
export interface TotpDisableRequest {
  email_code?: string // Email ownership proof.
  password?: string // Account authentication password.
}

// TotpVerificationMethod selects email or password proof for managing authenticator
// enrollment.
export interface TotpVerificationMethod {
  method: 'email' | 'password' // Account verification method.
}

// TotpLoginResponse indicates that authentication requires an additional authenticator
// code.
export interface TotpLoginResponse {
  requires_2fa: boolean // Additional authenticator verification required.
  temp_token?: string // Temporary login verification token.
  user_email_masked?: string // Masked account email.
}

// TotpLogin2FARequest supplies the temporary login token and the authenticator code.
export interface TotpLogin2FARequest {
  temp_token: string // Temporary login verification token.
  totp_code: string // Generated authenticator code.
}
