import type { User } from '@/api/identity.types'

// promo.types.ts describes registration promotions and the account bonuses granted
// through them. Administrators configure each code's bonus, usage limit, and expiration,
// while usage records identify the recipient and the amount credited when the promotion
// was redeemed.

// PromoCode describes a registration promotion and its granted balance and redemption
// limits.
export interface PromoCode {
  id: number // Record numeric identifier.
  code: string // Registration promotion code.
  bonus_amount: number // Granted promotional balance.
  max_uses: number // Maximum redemption count.
  used_count: number // Completed redemption count.
  status: 'active' | 'disabled' // Current lifecycle state.
  expires_at: string | null // Record expiration timestamp.
  notes: string | null // Administrator record notes.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// PromoCodeUsage records the account receiving a promotional bonus and its grant time.
export interface PromoCodeUsage {
  id: number // Record numeric identifier.
  promo_code_id: number // Redeemed promotion identifier.
  user_id: number // Owning account identifier.
  bonus_amount: number // Granted promotional balance.
  used_at: string // Latest redemption timestamp.
  user?: User // Expanded account details.
}

// CreatePromoCodeRequest supplies the bonus and eligibility limits for a new promotion.
export interface CreatePromoCodeRequest {
  code?: string // Requested promotion code.
  bonus_amount: number // Granted promotional balance.
  max_uses?: number // Maximum redemption count.
  expires_at?: number | null // Expiration time in Unix seconds.
  notes?: string // Administrator record notes.
}

// UpdatePromoCodeRequest supplies optional changes to a promotion's bonus and eligibility
// limits.
export interface UpdatePromoCodeRequest {
  code?: string // Replacement promotion code.
  bonus_amount?: number // Granted promotional balance.
  max_uses?: number // Maximum redemption count.
  status?: 'active' | 'disabled' // Current lifecycle state.
  expires_at?: number | null // Expiration time in Unix seconds.
  notes?: string // Administrator record notes.
}
