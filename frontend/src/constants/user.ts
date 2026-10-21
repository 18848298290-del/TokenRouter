// user.ts defines the largest API-key limit accepted by user forms. The value matches
// the maximum signed PostgreSQL INTEGER so browser-side validation can reject larger
// limits before sending them to the backend user settings field.

// MAX_USER_API_KEY_LIMIT bounds user form validation to the signed integer range
// supported by the persisted backend field.
export const MAX_USER_API_KEY_LIMIT = 2_147_483_647
