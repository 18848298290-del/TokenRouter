import type { PublicSettings } from '@/api/settings.types'

// global.d.ts declares public startup configuration injected into browser pages.
// Development and embedded serving can populate this optional value before Vue
// starts; consumers retain their API-loading path when the document has no
// injected configuration.

declare global {
  // Window exposes the optional server-provided settings snapshot to application initialization.
  interface Window {
    // Public settings injected before startup; absent documents load settings through the API instead.
    __APP_CONFIG__?: PublicSettings
  }
}

export {}
