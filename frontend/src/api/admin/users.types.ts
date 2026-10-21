import type { User } from '@/api/identity.types'

// users.types.ts defines administrative account details and profile update fields.
// Administrator responses add notes and per-group multipliers to the shared user profile,
// and an update can remove a user's custom group rate by sending a null entry.

// AdminUser extends account information with administrator notes, group rates, and live
// concurrency.
export interface AdminUser extends User {
  // Administrator notes returned by administrative user endpoints; ordinary user
  // responses omit this information entirely.
  notes: string // Administrator record notes.
  last_used_at?: string | null // Last used timestamp.
  // Per-user group multipliers map each group identifier to its billing rate multiplier.
  group_rates?: Record<number, number> // Per-group billing multipliers.
  // Current concurrent requests returned by the administrator user list; other user
  // endpoints omit this observation.
  current_concurrency?: number // Current concurrent request count.
}

// UpdateUserRequest supplies administrative changes to account access, balances, and
// group permissions.
export interface UpdateUserRequest {
  email?: string // Account email address.
  password?: string // Account authentication password.
  username?: string // Account display username.
  notes?: string // Administrator record notes.
  role?: 'admin' | 'user' // Account access role.
  balance?: number // Available wallet balance.
  concurrency?: number // Concurrent request ceiling.
  rpm_limit?: number // Requests per minute ceiling.
  api_key_limit?: number // Api key limit.
  status?: 'active' | 'disabled' // Current lifecycle state.
  allowed_groups?: number[] | null // Permitted exclusive group identifiers.
  disabled_public_groups?: number[] | null // Disabled public group identifiers.
  // Per-user group multipliers map each group identifier to a billing rate or null. A
  // null entry removes the user's custom multiplier for the corresponding group
  // identifier.
  group_rates?: Record<number, number | null> // Per-group billing multipliers.
}
