/**
 * apiError.ts extracts display messages and localization metadata from API
 * rejection shapes. Flat interceptor objects and nested response data use
 * different fields, so the helpers define their precedence before falling back
 * to a supplied message.
 *
 * Localized errors first select a reason or code, translate payment field
 * identifiers in a shallow metadata copy, and resolve the namespaced message.
 * Missing translations fall back to the original error-message selection.
 * Metadata localization preserves unrelated values and does not mutate the
 * received object. Error strings remain user-facing data; these helpers do not
 * log, sanitize, or classify them as trusted HTML.
 */

/**
 * ApiErrorLike represents the flat and nested rejection fields consumed by
 * error-message selection.
 */
interface ApiErrorLike {
  status?: number // HTTP or lookup status
  code?: number | string // machine-readable error code
  message?: string // displayable error message
  error?: string // reported error description
  reason?: string // granular backend reason
  metadata?: Record<string, unknown> // error interpolation values
  response?: {
    data?: {
      detail?: string // detailed response message
      message?: string // displayable error message
      code?: number | string // machine-readable error code
    } // response payload fields
  } // nested response wrapper
}

/**
 * extractApiErrorCode selects a reason, flat code, or nested response code in
 * that order. Nullish fields fall through, and the selected value becomes a
 * string; nonobjects return undefined.
 */
export function extractApiErrorCode(err: unknown): string | undefined {
  if (!err || typeof err !== 'object') return undefined
  const e = err as ApiErrorLike
  const code = e.reason ?? e.code ?? e.response?.data?.code
  return code != null ? String(code) : undefined
}

/**
 * extractApiErrorMetadata returns metadata from an object-shaped API
 * rejection. The object is returned by reference without validation or
 * copying, so consumers that rewrite interpolation values should copy it
 * first.
 */
export function extractApiErrorMetadata(err: unknown): Record<string, unknown> | undefined {
  if (!err || typeof err !== 'object') return undefined
  const e = err as ApiErrorLike
  return e.metadata
}

/**
 * TranslateFn translates an error key with optional interpolation metadata
 * values.
 */
type TranslateFn = (key: string, params?: Record<string, unknown>) => string
/**
 * TranslateWithExistsFn adds an optional translation-existence check to the
 * translator callable.
 */
type TranslateWithExistsFn = TranslateFn & {
  te?: (key: string) => boolean // optional translation-existence check
}

/**
 * tryTranslate resolves a translation key or returns the caller fallback. A
 * translation equal to its key or rejected by an optional existence check
 * preserves the original display value.
 */
function tryTranslate(t: TranslateFn, key: string, fallback: string): string {
  const translated = t(key)
  /**
   * An untranslated key is not a useful configuration-field label; retain the
   * original field name before attempting the optional translation-existence
   * check.
   */
  if (translated === key) return fallback
  const te = (t as TranslateWithExistsFn).te
  if (te && !te(key)) return fallback
  return translated
}

/**
 * localizeMetadata copies metadata and translates payment configuration key
 * labels for interpolation. The key field is translated once, while
 * slash-separated keys are translated individually; unrelated nested values
 * remain shared.
 */
function localizeMetadata(
  metadata: Record<string, unknown>,
  t: TranslateFn,
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...metadata }
  if (typeof out.key === 'string') {
    out.key = tryTranslate(t, `admin.settings.payment.field_${out.key}`, out.key)
  }
  if (typeof out.keys === 'string') {
    out.keys = out.keys
      .split('/')
      .map((k) => tryTranslate(t, `admin.settings.payment.field_${k}`, k))
      .join(' / ')
  }
  return out
}

/**
 * extractI18nErrorMessage resolves a namespaced API error translation with
 * localized metadata. Missing mappings use extractApiErrorMessage with the
 * supplied fallback; a translator existence check can accept a translation
 * identical to its key.
 */
export function extractI18nErrorMessage(
  err: unknown,
  t: TranslateFn,
  namespace: string,
  fallback: string,
): string {
  /**
   * Try the namespaced error translation with copied, localized interpolation
   * metadata before falling back to the message-selection rules for the original
   * rejection.
   */
  const code = extractApiErrorCode(err)
  if (code) {
    const key = `${namespace}.${code}`
    const rawMetadata = extractApiErrorMetadata(err) ?? {}
    const metadata = localizeMetadata(rawMetadata, t)
    const translated = t(key, metadata)

    if (translated !== key) return translated

    const te = (t as TranslateWithExistsFn).te
    if (te && te(key)) return translated
  }
  return extractApiErrorMessage(err, fallback)
}

/**
 * extractApiErrorMessage selects mapped text, flat messages, or nested
 * response messages for display. Falsy errors use the fallback immediately;
 * otherwise unknown values are stringified, with an ordinary object string
 * replaced by the fallback.
 */
export function extractApiErrorMessage(
  err: unknown,
  fallback = 'Unknown error',
  i18nMap?: Record<string, string>,
): string {
  if (!err) return fallback

  /**
   * An explicit code-to-message mapping takes priority over server prose so
   * callers can supply their already-localized text for known backend failures.
   */
  if (i18nMap) {
    const code = extractApiErrorCode(err)
    if (code && i18nMap[code]) return i18nMap[code]
  }

  if (typeof err === 'object' && err !== null) {
    const e = err as ApiErrorLike

    if (e.message) return e.message
    if (e.error) return e.error

    if (e.response?.data?.detail) return e.response.data.detail
    if (e.response?.data?.message) return e.response.data.message
  }

  if (err instanceof Error) return err.message

  /**
   * Unknown error values are stringified only after recognized message shapes
   * fail; replace the unhelpful plain-object string with the caller fallback.
   */
  const str = String(err)
  return str === '[object Object]' ? fallback : str
}
