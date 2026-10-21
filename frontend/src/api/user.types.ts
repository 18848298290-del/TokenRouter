// user.types.ts describes password changes and referral information exposed by the
// current-user endpoints. Referral balances distinguish transferable, frozen, and
// historical amounts, and each invitee records the rebate credited through that
// relationship for display in the account profile.

// AffiliateInvitee describes an invited account and its accumulated referral rebate.
export interface AffiliateInvitee {
  user_id: number // Owning account identifier.
  email: string // Account email address.
  username: string // Account display username.
  created_at?: string // Record creation timestamp.
  total_rebate: number // Accumulated referral rebate.
}

// UserAffiliateDetail summarizes the current user's referral relationship and rebate
// balances.
export interface UserAffiliateDetail {
  user_id: number // Owning account identifier.
  aff_code: string // Referral attribution code.
  inviter_id?: number | null // Referring account identifier.
  aff_count: number // Invited account count.
  aff_quota: number // Transferable referral allowance.
  aff_frozen_quota: number // Frozen referral allowance.
  aff_history_quota: number // Historical referral allowance.
  // Effective inviter rebate percentage; a user-specific override takes precedence over
  // the global rebate percentage.
  effective_rebate_rate_percent: number // Effective rebate rate percent.
  invitees: AffiliateInvitee[] // Invited account details.
}

// AffiliateTransferResponse reports transferred referral quota and the resulting wallet
// balance.
export interface AffiliateTransferResponse {
  transferred_quota: number // Transferred referral allowance.
  balance: number // Available wallet balance.
}

// ChangePasswordRequest supplies the current password and its requested replacement
// value.
export interface ChangePasswordRequest {
  old_password: string // Current account password.
  new_password: string // Replacement account password.
}
