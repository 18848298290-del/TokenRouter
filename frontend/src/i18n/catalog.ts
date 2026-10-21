import manifest from '../../../backend/internal/pkg/locale/manifest.json'

// catalog.ts maps browser language input and vendor widget codes through the
// shared server manifest. Canonical application codes identify message catalogs,
// while payment and CAPTCHA integrations receive the smaller language vocabulary
// supported by their own controls.

// LocaleCode represents an application language code from the shared manifest.
export type LocaleCode = (typeof manifest.locales)[number]['code']

// defaultLocale and availableLocales expose manifest defaults and selector
// metadata for application language selection.
export const defaultLocale = manifest.default as LocaleCode
export const availableLocales = manifest.locales.map((item) => ({
  ...item,
  code: item.code as LocaleCode,
  flag: item.code === 'en' ? '🇺🇸' : item.code === 'zh-Hans' ? '🇨🇳' : '🌐',
}))

// normalizeLocale returns the first supported canonical code matching the
// supplied language. Empty, malformed, overlong, and unsupported values return
// undefined so callers can select the appropriate interface default or original
// content.
export function normalizeLocale(value: string | null | undefined): LocaleCode | undefined {
  const raw = value?.trim().replace(/_/g, '-').toLowerCase()
  // Limit accepted tags to the shared language catalog validation length.
  if (!raw || raw.length > 35 || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(raw)) return undefined
  return availableLocales.find(
    (item) =>
      item.code.toLowerCase() === raw ||
      raw.startsWith(`${item.code.toLowerCase()}-`) ||
      item.aliases.some((alias) => alias.toLowerCase() === raw),
  )?.code
}

// VendorLocales describes the language codes accepted by each integrated browser
// widget.
type VendorLocales = {
  airwallex: 'zh' | 'en' // Airwallex payment widget language
  tencent: 'zh-cn' | 'en' // Tencent CAPTCHA widget language
  aliyun: 'cn' | 'en' // Aliyun CAPTCHA widget language
  stripe: 'zh' | 'en' // Stripe payment widget language
  turnstile: 'zh-cn' | 'en' // Cloudflare Turnstile widget language
}

// vendorLocale converts an application language into the requested vendor widget
// code. Simplified Chinese uses the vendor-specific Chinese spelling, while
// every other or unsupported application language selects English.
export function vendorLocale<V extends keyof VendorLocales>(
  vendor: V,
  code: string,
): VendorLocales[V] {
  // Widgets support these two choices, so unsupported application languages
  // select English using the same mapping as supported English.
  const chinese = normalizeLocale(code) === 'zh-Hans'
  const values: VendorLocales = {
    airwallex: chinese ? 'zh' : 'en',
    tencent: chinese ? 'zh-cn' : 'en',
    aliyun: chinese ? 'cn' : 'en',
    stripe: chinese ? 'zh' : 'en',
    turnstile: chinese ? 'zh-cn' : 'en',
  }
  return values[vendor]
}
