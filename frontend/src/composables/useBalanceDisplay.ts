import { computed, unref, type ComputedRef, type Ref } from 'vue'
import { getActivePinia } from 'pinia'
import { getLocale } from '@/i18n'
import { useAppStore } from '@/stores'

// useBalanceDisplay.ts resolves balance labels and formats amounts using the active
// locale. Overrides take precedence over public settings and injected startup
// configuration, with blank resolved labels replaced by USD defaults. Formatting also
// supports a fixed USD symbol for displays that must identify that unit.

// MaybeRef accepts a plain value or a Vue reactive value.
type MaybeRef<T> = T | Ref<T> | ComputedRef<T>

// BalanceDisplayOverrides supplies optional reactive replacements for configured balance
// presentation.
interface BalanceDisplayOverrides {
  // unitName, unitSymbol, and iconSvg accept reactive overrides. Nullish overrides defer
  // to settings and startup configuration; empty strings remain selected values before
  // trimming and display defaults apply.
  unitName?: MaybeRef<string | null | undefined> // balance unit name override
  unitSymbol?: MaybeRef<string | null | undefined> // balance unit symbol override
  iconSvg?: MaybeRef<string | null | undefined> // balance icon SVG override
}

// BalanceFormatOptions controls precision, grouping, symbols, and missing amount text.
interface BalanceFormatOptions {
  // fractionDigits sets both minimum and maximum fractional precision; omission uses
  // amount magnitude to choose the default.
  fractionDigits?: number // fixed fractional digit count
  // withSymbol omits the prefix only when false, including when displaying the default
  // missing-value placeholder.
  withSymbol?: boolean // currency symbol inclusion switch
  // fallback replaces absent or nonfinite amounts verbatim; the formatter adds no currency
  // symbol to that supplied text.
  fallback?: string // missing amount display text
  // useGrouping controls locale-specific grouping in Intl.NumberFormat and defaults to
  // true when no value is supplied.
  useGrouping?: boolean // numeric digit grouping switch
}

// USD_UNIT_NAME and USD_UNIT_SYMBOL provide the default labels and fixed currency
// formatting symbols.
const USD_UNIT_NAME = 'USD'
const USD_UNIT_SYMBOL = '$'

// normalizeDisplayText trims a display label and replaces an empty result with the
// supplied default. Null and undefined values follow the same replacement path as
// whitespace.
function normalizeDisplayText(value: string | null | undefined, fallback: string): string {
  const normalized = value?.trim() ?? ''
  return normalized || fallback
}

// useBalanceDisplay exposes reactive currency labels and locale-aware amount formatters.
// Create it after Pinia activation to read store settings; otherwise its configuration
// comes from the captured startup object and supplied overrides.
export function useBalanceDisplay(overrides: BalanceDisplayOverrides = {}) {
  const appStore = getActivePinia() ? useAppStore() : null
  const injectedConfig = typeof window !== 'undefined' ? window.__APP_CONFIG__ : undefined

  // balanceUnitName, balanceUnitSymbol, and balanceIconSvg react to supplied overrides and
  // store settings; startup configuration is captured when the composable is created.
  const balanceUnitName = computed(() =>
    normalizeDisplayText(
      unref(overrides.unitName) ??
        appStore?.cachedPublicSettings?.balance_unit_name ??
        injectedConfig?.balance_unit_name,
      USD_UNIT_NAME,
    ),
  )

  const balanceUnitSymbol = computed(() =>
    normalizeDisplayText(
      unref(overrides.unitSymbol) ??
        appStore?.cachedPublicSettings?.balance_unit_symbol ??
        injectedConfig?.balance_unit_symbol,
      USD_UNIT_SYMBOL,
    ),
  )

  const balanceIconSvg = computed(() => {
    const svg =
      unref(overrides.iconSvg) ??
      appStore?.cachedPublicSettings?.balance_icon_svg ??
      injectedConfig?.balance_icon_svg ??
      ''
    return svg.trim()
  })

  const hasCustomBalanceIcon = computed(() => balanceIconSvg.value.length > 0)

  // formatAmount formats finite amounts with the selected unit symbol and locale. Missing
  // or nonfinite amounts use caller replacement text or a two-decimal zero; small nonzero
  // amounts default to six fractional digits.
  function formatAmount(
    value: number | null | undefined,
    unitSymbol: string,
    options: BalanceFormatOptions = {},
  ): string {
    if (value == null) {
      return options.fallback ?? `${options.withSymbol === false ? '' : unitSymbol}0.00`
    }

    const amount = Number(value)
    // Invalid numeric inputs use the same display placeholder as absent amounts so amount
    // labels remain renderable for unavailable data.
    if (!Number.isFinite(amount)) {
      return options.fallback ?? `${options.withSymbol === false ? '' : unitSymbol}0.00`
    }

    // Amounts below one cent retain additional visible precision.
    const fractionDigits =
      options.fractionDigits ?? (Math.abs(amount) > 0 && Math.abs(amount) < 0.01 ? 6 : 2)
    const formatted = new Intl.NumberFormat(getLocale(), {
      minimumFractionDigits: fractionDigits,
      maximumFractionDigits: fractionDigits,
      useGrouping: options.useGrouping ?? true,
    }).format(amount)

    if (options.withSymbol === false) {
      return formatted
    }
    return `${unitSymbol}${formatted}`
  }

  // formatBalanceAmount formats an amount using the currently resolved balance symbol.
  // Precision, missing-value text, grouping, and symbol omission are delegated to
  // formatAmount with the supplied options.
  function formatBalanceAmount(
    value: number | null | undefined,
    options: BalanceFormatOptions = {},
  ): string {
    return formatAmount(value, balanceUnitSymbol.value, options)
  }

  // formatUsdAmount formats an amount using the fixed dollar currency symbol. It accepts
  // the same precision and missing-value options as balance formatting, independent of
  // configured balance labels.
  function formatUsdAmount(
    value: number | null | undefined,
    options: BalanceFormatOptions = {},
  ): string {
    return formatAmount(value, USD_UNIT_SYMBOL, options)
  }

  return {
    balanceUnitName,
    balanceUnitSymbol,
    usdUnitName: USD_UNIT_NAME,
    usdUnitSymbol: USD_UNIT_SYMBOL,
    balanceIconSvg,
    hasCustomBalanceIcon,
    formatBalanceAmount,
    formatUsdAmount,
  }
}
