// ui.ts defines shared copy-feedback and search-debounce durations in milliseconds.
// These values control how long feedback remains visible and when list searches begin;
// animation transitions use the separate motion variables supplied by the global
// stylesheet.

// COPY_FEEDBACK_MS controls the shared copied-state display duration used by clipboard
// buttons and copyable value rows.
export const COPY_FEEDBACK_MS = 2000

// SEARCH_DEBOUNCE_MS controls how long list search inputs wait after typing before
// starting another search request.
export const SEARCH_DEBOUNCE_MS = 300
