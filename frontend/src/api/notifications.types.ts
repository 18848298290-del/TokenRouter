// notifications.types.ts describes notification email destinations and their verification
// state. An empty email identifies the primary user or administrator address, so
// notification settings retain that placeholder alongside additional addresses and their
// individual disabled and verified flags.

// NotifyEmailEntry records a notification destination and its delivery eligibility.
export interface NotifyEmailEntry {
  // An empty string selects the primary user or administrator email; additional entries
  // contain their own destination email addresses.
  email: string // Notification destination address.
  disabled: boolean // Delivery disabled flag.
  verified: boolean // Address verification state.
}
