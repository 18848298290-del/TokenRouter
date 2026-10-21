/**
 * index.ts exports browser API modules and shared URL builders from one entry
 * point. Domain API objects retain their runtime exports, selected local types
 * remain available alongside them, and the shared HTTP client is also the
 * default export.
 */

export { apiClient, buildApiUrl, buildGatewayUrl } from './client'

// Auth API
export { authAPI, isTotp2FARequired, type LoginResponse } from './auth'

// User APIs
export { keysAPI } from './keys'
export { usageAPI } from './usage'
export { userAPI } from './user'
export { redeemAPI, type RedeemHistoryItem } from './redeem'
export { paymentAPI } from './payment'
export { userGroupsAPI } from './groups'
export * as batchImageAPI from './batchImage'
export { totpAPI } from './totp'
export { teamAPI } from './team'
export { marketplaceAPI } from './marketplace'
export * from './creative'
export { passkeyAPI, type PasskeyCredentialSummary } from './passkey'
export { default as announcementsAPI } from './announcements'

// Admin APIs
export { adminAPI } from './admin'

// Default export
export { default } from './client'
