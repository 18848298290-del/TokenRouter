/**
 * tablePreferences.ts normalizes table pagination preferences against optional
 * application configuration. Configured defaults and option lists are
 * validated independently, so a valid default need not itself be one of the
 * selectable options.
 *
 * Selection normalization maps the requested size upward to the first
 * sufficient configured option, or to the largest option when none is
 * sufficient. Invalid selections first use the configured default. If every
 * configured option is invalid, built-in options supply a nonempty set. The
 * helpers do not mutate the injected configuration or shared default list;
 * callers receive a fresh sorted list for configured options and a copy for
 * built-in fallback.
 */

/**
 * Configuration bounds validate defaults and options, while the built-in
 * default and option array supply fallback choices when injected configuration
 * is unusable.
 */
const MIN_TABLE_PAGE_SIZE = 5
const MAX_TABLE_PAGE_SIZE = 1000

export const DEFAULT_TABLE_PAGE_SIZE = 20
export const DEFAULT_TABLE_PAGE_SIZE_OPTIONS = [10, 20, 50, 100]

/**
 * sanitizePageSize coerces a configured value to a whole size within five
 * through one thousand. Invalid values return null so configuration readers
 * can distinguish unusable entries from accepted numeric preferences.
 */
const sanitizePageSize = (value: unknown): number | null => {
  const size = Number(value)
  if (!Number.isInteger(size)) return null
  if (size < MIN_TABLE_PAGE_SIZE || size > MAX_TABLE_PAGE_SIZE) return null
  return size
}

/**
 * parsePageSizeForSelection coerces a requested value to an integer of at
 * least five. Large selections remain valid here because the later option
 * mapping clamps them to the largest available choice.
 */
const parsePageSizeForSelection = (value: unknown): number | null => {
  const size = Number(value)
  if (!Number.isInteger(size)) return null
  if (size < MIN_TABLE_PAGE_SIZE) return null
  return size
}

/**
 * getInjectedAppConfig returns the browser-injected application configuration
 * or null without window.
 */
const getInjectedAppConfig = () => {
  if (typeof window === 'undefined') return null
  return window.__APP_CONFIG__ ?? null
}

/**
 * getSanitizedConfiguredOptions returns sorted unique valid configured page
 * sizes. Missing or non-array configuration returns an empty list, leaving the
 * public option reader to choose the built-in fallback values.
 */
const getSanitizedConfiguredOptions = (): number[] => {
  const configured = getInjectedAppConfig()?.table_page_size_options
  if (!Array.isArray(configured)) return []

  return Array.from(
    new Set(
      configured
        .map((value) => sanitizePageSize(value))
        .filter((value): value is number => value !== null),
    ),
  ).sort((a, b) => a - b)
}

/**
 * normalizePageSizeToOptions rounds a size upward within sorted nonempty
 * options. Values exceeding every option use the final option; callers supply
 * the validated nonempty ordering established by the option reader.
 */
const normalizePageSizeToOptions = (value: number, options: number[]): number => {
  for (const option of options) {
    if (option >= value) {
      return option
    }
  }
  /**
   * Requests larger than all available options use the largest selectable size,
   * keeping persisted preferences representable by the current table control.
   */
  return options[options.length - 1]
}

/**
 * getConfiguredTableDefaultPageSize reads a valid configured default or the
 * built-in default. It does not force that number into the selectable options,
 * leaving that decision to selection normalization.
 */
export const getConfiguredTableDefaultPageSize = (): number => {
  const configured = sanitizePageSize(getInjectedAppConfig()?.table_default_page_size)
  if (configured === null) {
    return DEFAULT_TABLE_PAGE_SIZE
  }
  return configured
}

/**
 * getConfiguredTablePageSizeOptions returns sanitized configured choices or a
 * copy of built-in choices. Empty and wholly invalid lists both select the
 * fallback, so callers can safely normalize against a nonempty array.
 */
export const getConfiguredTablePageSizeOptions = (): number[] => {
  const unique = getSanitizedConfiguredOptions()
  if (unique.length === 0) {
    /**
     * A missing or wholly invalid configured list uses a fresh copy of built-in
     * options, giving selection normalization a nonempty ordered list.
     */
    return [...DEFAULT_TABLE_PAGE_SIZE_OPTIONS]
  }

  return unique.length > 0 ? unique : [...DEFAULT_TABLE_PAGE_SIZE_OPTIONS]
}

/**
 * normalizeTablePageSize selects an available option for a requested page
 * size. It first separates valid selection input from invalid input, then
 * obtains the configured default and sanitized options independently.
 *
 * A valid requested number rounds upward to the first sufficient option or
 * clamps to the largest one. Invalid input follows the same mapping using the
 * default, which may not itself appear in the options. This keeps displayed
 * pagination choices consistent without rewriting the independently configured
 * default value.
 */
export const normalizeTablePageSize = (value: unknown): number => {
  const normalized = parsePageSizeForSelection(value)
  const defaultSize = getConfiguredTableDefaultPageSize()
  const options = getConfiguredTablePageSizeOptions()
  if (normalized !== null) {
    return normalizePageSizeToOptions(normalized, options)
  }
  return normalizePageSizeToOptions(defaultSize, options)
}
