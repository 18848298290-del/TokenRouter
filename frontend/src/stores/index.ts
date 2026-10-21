// index.ts exports the application Pinia stores used by callers that need several state
// modules. The export order follows authentication, application settings, subscriptions,
// onboarding, announcements, and payments, while domain records are imported directly
// from their API or UI owner.

export { useAuthStore } from './auth'
export { useAppStore } from './app'
export { useAdminSettingsStore } from './adminSettings'
export { useSubscriptionStore } from './subscriptions'
export { useOnboardingStore } from './onboarding'
export { useAnnouncementStore } from './announcements'
export { usePaymentStore } from './payment'
