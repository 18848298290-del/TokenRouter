import type { NotifyEmailEntry } from '@/api/notifications.types'
import type { UserSubscription } from '@/api/subscriptions.types'

// identity.types.ts describes account profiles and the external identities supplying
// profile values. Identity bindings can contain a simple availability flag or detailed
// provider information, and subscription details may be included with the account
// returned by an endpoint.

// UserAuthProvider identifies a supported provider for an account identity binding.
export type UserAuthProvider =
  'email' | 'linuxdo' | 'oidc' | 'wechat' | 'github' | 'google' | 'dingtalk'

// UserAuthBindingStatus describes the provider identity bound to an account.
export interface UserAuthBindingStatus {
  bound?: boolean // Identity binding present.
  bound_count?: number // Bound identity count.
  provider?: UserAuthProvider | string // Identity provider identifier.
  provider_key?: string | null // Identity provider configuration key.
  provider_subject?: string | null // Identity subject identifier.
  issuer?: string | null // Identity token issuer.
  label?: string | null // Human-readable display label.
  provider_label?: string | null // Identity provider display label.
  display_name?: string | null // Human-readable display name.
  subject_hint?: string | null // Masked identity subject.
  verified_at?: string | null // Identity verification timestamp.
  bind_start_path?: string | null // Identity binding start path.
  can_bind?: boolean // Binding action availability.
  can_unbind?: boolean // Unbinding action availability.
  note_key?: string | null // Localized note message key.
  note?: string | null // Additional display explanation.
  metadata?: Record<string, unknown> // Provider-specific identity details.
}

// UserProfileSourceContext identifies the provider supplying a displayed profile value.
export interface UserProfileSourceContext {
  provider?: UserAuthProvider | string // Identity provider identifier.
  source?: string | null // Observed information source.
  label?: string | null // Human-readable display label.
  provider_label?: string | null // Identity provider display label.
}

// User describes account access, billing state, and profile data returned to clients.
export interface User {
  preferred_locale?: string | null // Preferred interface locale.
  id: number // Record numeric identifier.
  username: string // Account display username.
  email: string // Account email address.
  avatar_url?: string | null // Account avatar URL.
  avatar_source?: string | UserProfileSourceContext | null // Avatar identity source.
  username_source?: string | UserProfileSourceContext | null // Username identity source.
  display_name_source?: string | UserProfileSourceContext | null // Display-name identity source.
  nickname_source?: string | UserProfileSourceContext | null // Nickname identity source.
  profile_sources?: {
    avatar?: string | UserProfileSourceContext | null // Avatar source identity.
    username?: string | UserProfileSourceContext | null // Username source identity.
    display_name?: string | UserProfileSourceContext | null // Display-name source identity.
    nickname?: string | UserProfileSourceContext | null // Account nickname source.
  } // Profile field origins.
  auth_bindings?: Partial<Record<UserAuthProvider, boolean | UserAuthBindingStatus>> // Authentication provider bindings.
  identity_bindings?: Partial<Record<UserAuthProvider, boolean | UserAuthBindingStatus>> // External identity bindings.
  email_bound?: boolean // Email identity bound.
  linuxdo_bound?: boolean // Linux.do identity bound.
  oidc_bound?: boolean // OIDC identity bound.
  wechat_bound?: boolean // WeChat identity bound.
  role: 'admin' | 'user' // Assigned account access role.
  balance: number // Available account wallet balance.
  frozen_balance?: number // Balance currently reserved for asynchronous batch tasks.
  concurrency: number // Maximum concurrent account requests.
  // A positive user RPM ceiling is checked independently after the effective
  // user-group ceiling. Zero disables only this user gate; group limits can still apply.
  rpm_limit?: number // User request rate ceiling.
  // This ceiling limits how many API Keys the account may create; zero permits an
  // unlimited number of keys.
  api_key_limit: number // API Key count ceiling.
  status: 'active' | 'disabled' // Current account access status.
  allowed_groups: number[] | null // Permitted exclusive group identifiers.
  disabled_public_groups?: number[] | null // Disabled public group identifiers.
  balance_notify_enabled: boolean // Low-balance notification switch.
  balance_notify_threshold: number | null // Low-balance notification threshold.
  balance_notify_extra_emails: NotifyEmailEntry[] // Additional balance notification recipients.
  subscriptions?: UserSubscription[] // Included account subscription details.
  last_active_at?: string | null // Last active timestamp.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
  deleted_at?: string | null // Account deletion timestamp.
}
