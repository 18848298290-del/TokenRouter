// layout.ts defines browser-side responsive thresholds shared by media-query consumers
// and table layouts. Values correspond to Tailwind's default small, medium, and large
// screens; maximum queries subtract one pixel so adjacent minimum and maximum ranges do
// not overlap.

// Responsive breakpoints match Tailwind screen minima and provide a numeric source for
// browser-side viewport decisions.
export const BREAKPOINT_SM = 640
export const BREAKPOINT_MD = 768
export const BREAKPOINT_LG = 1024

// Media-query strings share the numeric thresholds; maximum queries end one pixel before
// the corresponding minimum to keep adjacent ranges exclusive.
export const MEDIA_MIN_MD = `(min-width: ${BREAKPOINT_MD}px)`
export const MEDIA_MIN_LG = `(min-width: ${BREAKPOINT_LG}px)`
export const MEDIA_MAX_SM = `(max-width: ${BREAKPOINT_SM - 1}px)`
export const MEDIA_MAX_MD = `(max-width: ${BREAKPOINT_MD - 1}px)`

// TABLE_DESKTOP_MIN_WIDTH selects the desktop table layout; narrower viewports use the
// shared per-row mobile card presentation.
export const TABLE_DESKTOP_MIN_WIDTH = BREAKPOINT_LG

// TABLE_DESKTOP_MEDIA_QUERY keeps the table container and its content on the same
// desktop-layout media query.
export const TABLE_DESKTOP_MEDIA_QUERY = MEDIA_MIN_LG
