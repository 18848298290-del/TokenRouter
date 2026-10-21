import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import {
  tablePageSizeMin,
  tablePageSizeMax,
  usageRankingLimitMin,
  usageRankingLimitMax,
  marketplaceAvailabilityWindowDaysMin,
  marketplaceAvailabilityWindowDaysMax,
  marketplaceAvailabilityWindowDaysDefault,
  marketplaceAvailabilityBucketMinutesMin,
  marketplaceAvailabilityBucketMinutesMax,
  marketplaceAvailabilityBucketMinutesDefault,
} from './settingsDefaults'
import type { useGeneralSettings } from './useGeneralSettings'
import type { SettingsForm } from './settingsState'

/**
 * generalSettingsValidation.ts validates pagination, ranking, balance display,
 * and optional-link values before the administrator bulk update. Pagination and
 * ranking errors stop the display phase before its accepted values replace the
 * shared draft. After those checks succeed, balance text, conversion rates, and
 * marketplace observation intervals are normalized for the domain serializer.
 *
 * The payload coordinator invokes display normalization early and optional URL
 * cleanup after identity capability validation. Keeping those operations separate
 * preserves the first reported error and the draft mutations already visible when
 * a later field fails. Optional malformed links become empty strings because the
 * root form disables native validation across its hidden tabs.
 */

/**
 * createGeneralSettingsValidation binds pagination, ranking, and optional URL checks to
 * the reactive page draft. Create it during setup, then call its returned operations in
 * coordinator order so accepted normalizations precede their corresponding serializers.
 */
export function createGeneralSettingsValidation(
  form: Pick<
    SettingsForm,
    | 'table_default_page_size'
    | 'usage_ranking_limit'
    | 'table_page_size_options'
    | 'balance_unit_name'
    | 'balance_unit_symbol'
    | 'balance_icon_svg'
    | 'reasoning_point_rmb_unit_price'
    | 'usd_exchange_rate'
    | 'marketplace_availability_window_days'
    | 'marketplace_availability_bucket_minutes'
    | 'frontend_url'
    | 'doc_url'
  >,
  actions: Pick<
    ReturnType<typeof useGeneralSettings>,
    | 'tablePageSizeOptionsInput'
    | 'ensureUsageRankingSortMetricVisible'
    | 'parseTablePageSizeOptionsInput'
  >,
) {
  // Validation feedback uses the page locale and toast store, while controller
  // actions operate on the same draft captured by this factory.
  const { t } = useI18n()
  const appStore = useAppStore()
  const {
    tablePageSizeOptionsInput,
    ensureUsageRankingSortMetricVisible,
    parseTablePageSizeOptionsInput,
  } = actions

  /**
   * normalizeDisplaySettings validates pagination and ranking limits, then updates
   * the display draft and its bounded availability intervals. Invalid page size,
   * page-size options, or ranking count reports the first matching error and returns
   * false before those draft display values are assigned. Accepted values become
   * the shared draft used by domain serializers. The selected ranking metric is
   * made visible, balance text receives defaults, and nonnegative conversion rates
   * and clamped time intervals are retained for the save attempt.
   */
  function normalizeDisplaySettings(): boolean {
    // Page size and ranking limits use whole counts accepted by their controls.
    // Validate all three inputs before replacing the related draft display values.
    const normalizedTableDefaultPageSize = Math.floor(Number(form.table_default_page_size))

    if (
      !Number.isInteger(normalizedTableDefaultPageSize) ||
      normalizedTableDefaultPageSize < tablePageSizeMin ||
      normalizedTableDefaultPageSize > tablePageSizeMax
    ) {
      appStore.showError(
        t('admin.settings.site.tableDefaultPageSizeRangeError', {
          min: tablePageSizeMin,
          max: tablePageSizeMax,
        }),
      )
      return false
    }

    const normalizedTablePageSizeOptions = parseTablePageSizeOptionsInput(
      tablePageSizeOptionsInput.value,
    )

    if (!normalizedTablePageSizeOptions) {
      appStore.showError(
        t('admin.settings.site.tablePageSizeOptionsFormatError', {
          min: tablePageSizeMin,
          max: tablePageSizeMax,
        }),
      )
      return false
    }

    const normalizedUsageRankingLimit = Math.floor(Number(form.usage_ranking_limit))

    if (
      !Number.isInteger(normalizedUsageRankingLimit) ||
      normalizedUsageRankingLimit < usageRankingLimitMin ||
      normalizedUsageRankingLimit > usageRankingLimitMax
    ) {
      appStore.showError(
        t('admin.settings.site.usageRankingLimitRangeError', {
          min: usageRankingLimitMin,
          max: usageRankingLimitMax,
        }),
      )
      return false
    }

    form.table_default_page_size = normalizedTableDefaultPageSize

    form.table_page_size_options = normalizedTablePageSizeOptions

    form.usage_ranking_limit = normalizedUsageRankingLimit

    ensureUsageRankingSortMetricVisible()

    // Empty balance labels select the existing USD display defaults before the
    // payload is built, so saved presentation values stay usable for readers.
    form.balance_unit_name = form.balance_unit_name.trim() || 'USD'

    form.balance_unit_symbol = form.balance_unit_symbol.trim() || '$'

    form.balance_icon_svg = form.balance_icon_svg.trim()

    // Display conversion rates remain nonnegative, and availability intervals
    // are clamped to the same documented bounds used by the settings controls.
    form.reasoning_point_rmb_unit_price = Math.max(
      0,
      Number(form.reasoning_point_rmb_unit_price) || 0,
    )

    form.usd_exchange_rate = Math.max(0, Number(form.usd_exchange_rate) || 0)

    form.marketplace_availability_window_days = Math.min(
      marketplaceAvailabilityWindowDaysMax,
      Math.max(
        marketplaceAvailabilityWindowDaysMin,
        Math.floor(
          Number(form.marketplace_availability_window_days) ||
            marketplaceAvailabilityWindowDaysDefault,
        ),
      ),
    )

    form.marketplace_availability_bucket_minutes = Math.min(
      marketplaceAvailabilityBucketMinutesMax,
      Math.max(
        marketplaceAvailabilityBucketMinutesMin,
        Math.floor(
          Number(form.marketplace_availability_bucket_minutes) ||
            marketplaceAvailabilityBucketMinutesDefault,
        ),
      ),
    )
    return true
  }

  /**
   * normalizeOptionalUrls clears malformed optional frontend and documentation URLs.
   * The root form disables native validation for cross-tab saves, so this cleanup
   * lets the bulk request continue with empty links when those optional inputs
   * are invalid.
   */
  function normalizeOptionalUrls(): void {
    /**
     * isValidHttpUrl accepts an empty optional URL or an HTTP(S) address.
     * Malformed input returns false, allowing the caller to clear optional links
     * before submitting the form whose native validation is disabled.
     */
    const isValidHttpUrl = (url: string): boolean => {
      if (!url) return true
      try {
        const u = new URL(url)
        return u.protocol === 'http:' || u.protocol === 'https:'
      } catch {
        // A URL parser failure represents invalid optional input; returning false
        // lets the caller clear the field while continuing validation of other settings.
        return false
      }
    }

    // Native validation is disabled for cross-tab saving. Clear malformed optional
    // links here so those values do not reject the entire bulk request.
    if (!isValidHttpUrl(form.frontend_url)) form.frontend_url = ''

    if (!isValidHttpUrl(form.doc_url)) form.doc_url = ''
  }
  return { normalizeDisplaySettings, normalizeOptionalUrls }
}
