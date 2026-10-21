/**
 * billingMode.ts selects billing labels and estimates per-image charges for usage rows.
 * Image counts can identify image billing when the stored mode is absent or ambiguous,
 * but explicit token and video modes take precedence over that inference.
 *
 * Display classification combines the mode with a positive image count because those
 * fields can coexist on records billed in different units. The unit-price helper
 * divides the recorded total by the recorded count and returns zero when the row cannot
 * provide a finite result. Translation and badge helpers share token mode as their
 * default for unknown mode strings.
 */

/**
 * BILLING_MODE_TOKEN and the adjacent mode identifiers select token, request, image,
 * and video presentation branches throughout usage displays.
 */
export const BILLING_MODE_TOKEN = 'token'
export const BILLING_MODE_PER_REQUEST = 'per_request'
export const BILLING_MODE_IMAGE = 'image'
export const BILLING_MODE_VIDEO = 'video'

/**
 * getBillingModeLabel translates a billing mode for usage tables. Missing or
 * unrecognized modes use the token label, and the caller supplies the translator for
 * its current locale.
 */
export function getBillingModeLabel(
  mode: string | null | undefined,
  t: (key: string) => string,
): string {
  switch (mode) {
    case BILLING_MODE_PER_REQUEST:
      return t('admin.usage.billingModePerRequest')
    case BILLING_MODE_IMAGE:
      return t('admin.usage.billingModeImage')
    case BILLING_MODE_VIDEO:
      return t('admin.usage.billingModeVideo')
    default:
      return t('admin.usage.billingModeToken')
  }
}

/**
 * getBillingModeBadgeClass returns light and dark theme classes for a billing mode.
 * Missing or unrecognized strings select the blue token badge used by the default
 * display branch.
 */
export function getBillingModeBadgeClass(mode: string | null | undefined): string {
  switch (mode) {
    case BILLING_MODE_PER_REQUEST:
      return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
    case BILLING_MODE_IMAGE:
      return 'bg-pink-100 text-pink-700 dark:bg-pink-900/30 dark:text-pink-300'
    case BILLING_MODE_VIDEO:
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default:
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  }
}

/**
 * ImageBillingRow supplies counts, mode, and total charge for image presentation.
 */
interface ImageBillingRow {
  /**
   * image_count and billing_mode jointly select image presentation: a positive
   * count permits inference unless the recorded mode is token or video. Missing
   * mode information leaves that inference available.
   */
  image_count: number // recorded image count
  billing_mode?: string | null // stored billing unit
  /**
   * total_cost is the recorded total charge in the row currency; imageUnitPrice
   * substitutes zero when it is absent.
   */
  total_cost?: number | null // total recorded charge
}

/**
 * isImageUsage reports whether a row should use image billing presentation. A positive
 * image count is necessary, and explicit token or video billing prevents image
 * inference even when images were recorded.
 *
 * The mode and count are evaluated together because an image-bearing request can still
 * be billed using another unit. Missing rows and nonpositive counts return false; other
 * mode strings, including per_request, allow a positive count to select image
 * presentation.
 */
export function isImageUsage(
  row: Pick<ImageBillingRow, 'image_count' | 'billing_mode'> | null | undefined,
): boolean {
  return (
    (row?.image_count ?? 0) > 0 &&
    row?.billing_mode !== BILLING_MODE_TOKEN &&
    row?.billing_mode !== BILLING_MODE_VIDEO
  )
}

/**
 * getDisplayBillingMode returns image mode when isImageUsage accepts the row. Otherwise
 * it returns the stored mode unchanged, including null or undefined, so label consumers
 * retain their own defaults.
 */
export function getDisplayBillingMode(
  row: Pick<ImageBillingRow, 'billing_mode' | 'image_count'> | null | undefined,
): string | null | undefined {
  if (isImageUsage(row)) {
    return BILLING_MODE_IMAGE
  }
  return row?.billing_mode
}

/**
 * imageUnitPrice divides the recorded total charge by the positive image count. Missing
 * rows, nonpositive counts, absent totals, and nonfinite quotients return zero in the
 * same currency as total_cost.
 */
export function imageUnitPrice(
  row: Pick<ImageBillingRow, 'image_count' | 'total_cost'> | null,
): number {
  if (!row || row.image_count <= 0) return 0
  const total = row.total_cost ?? 0
  const price = total / row.image_count
  /**
   * A malformed total or count can produce a nonfinite quotient; display zero so the
   * per-image amount remains numeric.
   */
  return Number.isFinite(price) ? price : 0
}
