import { getLocale } from '@/i18n'

/**
 * orderUtils.ts formats order timestamps for payment tables and dialogs using the current
 * application locale. The shared empty-value marker lets order lifecycle fields remain
 * readable before their corresponding events have occurred.
 */

/**
 * formatOrderDateTime renders an order timestamp in the active interface locale. An empty
 * timestamp displays a dash so rows and dialogs can represent an event that has not
 * occurred.
 */
export function formatOrderDateTime(dateStr: string): string {
  if (!dateStr) return '-'
  return new Date(dateStr).toLocaleString(getLocale())
}
