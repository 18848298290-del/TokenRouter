/**
 * ccswitchImport.ts builds CCSwitch provider import links and their embedded
 * usage scripts. The selected client determines the application identifier and
 * endpoint suffix, while the chosen model and API key remain input data passed
 * to the external client.
 *
 * Usage queries are pinned to the API origin for parseable URLs, with legacy
 * suffix normalization for malformed configuration. The JavaScript script
 * escapes embedded string literals, then the deeplink encodes its UTF-8 bytes
 * as base64 before query encoding. That separation preserves non-ASCII script
 * text through the URL round trip. Generated links contain credentials and
 * should be handed directly to the intended client.
 */

/**
 * CcSwitchClientType identifies the supported client destinations offered by
 * CCSwitch import.
 */
export type CcSwitchClientType = 'claude' | 'codex' | 'gemini' | 'grok'

/**
 * CcSwitchImportConfig holds the selected client application, endpoint, and
 * optional model.
 */
export interface CcSwitchImportConfig {
  app: string // CCSwitch application identifier
  endpoint: string // client API endpoint
  model?: string // selected request model
}

/**
 * CcSwitchImportDeeplinkInput supplies provider settings and script content
 * for credential-bearing import links.
 */
export interface CcSwitchImportDeeplinkInput {
  baseUrl: string // gateway base URL
  model: string // selected request model
  clientType: CcSwitchClientType // selected import client
  providerName: string // exported provider label
  apiKey: string // exported API credential
  usageScript: string // CCSwitch usage-script source
}

/**
 * encodeBase64Utf8 encodes script text as UTF-8 bytes before producing base64.
 * Chunked character conversion avoids passing the complete byte array as
 * function arguments, while the final encoded string retains all chunks.
 */
const encodeBase64Utf8 = (value: string) => {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  /**
   * Bound byte spreading to avoid oversized argument lists.
   */
  const chunkSize = 0x8000

  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunkSize))
  }

  return btoa(binary)
}

/**
 * stripTrailingV1 removes trailing slashes and one terminal v1 path segment.
 */
const stripTrailingV1 = (value: string) => value.replace(/\/+$/, '').replace(/\/v1$/, '')

/**
 * toJsStringLiteral builds a quoted value for insertion into the generated
 * CCSwitch JavaScript source. An empty input selects USD, matching the script
 * fallback convention used by its callers.
 *
 * JSON serialization supplies the ordinary string escaping, then Unicode line
 * separators receive explicit escape sequences. This keeps embedded values
 * inside a string literal when the source is interpreted by the receiving
 * client. Callers insert the returned literal directly and must not quote it a
 * second time.
 */
const toJsStringLiteral = (value: string) =>
  /**
   * Encode embedded values as JavaScript string literals before insertion into
   * script source, including line separators that could otherwise split
   * generated source lines.
   */
  JSON.stringify(value || 'USD')
    .replace(/\u2028/g, '\\u2028')
    .replace(/\u2029/g, '\\u2029')

/**
 * resolveCcSwitchUsageUrl builds the usage endpoint from a parseable base URL
 * origin. Invalid URLs use legacy suffix normalization instead, so this helper
 * preserves compatibility rather than validating an import destination.
 */
const resolveCcSwitchUsageUrl = (baseUrl: string): string => {
  try {
    return `${new URL(baseUrl).origin}/v1/usage`
  } catch {
    /**
     * Unparseable legacy configuration retains suffix-based endpoint construction,
     * leaving destination validation to the caller while preserving the existing
     * export fallback.
     */
    return `${stripTrailingV1(baseUrl)}/v1/usage`
  }
}

/**
 * buildCcSwitchUsageScript creates the CCSwitch request and extractor source
 * with a fixed usage endpoint. Endpoint and fallback currency are escaped as
 * literals, while the API key remains a CCSwitch substitution placeholder.
 */
export function buildCcSwitchUsageScript(baseUrl: string, fallbackUnit: string): string {
  const usageUrlLiteral = toJsStringLiteral(resolveCcSwitchUsageUrl(baseUrl))
  const fallbackUnitLiteral = toJsStringLiteral(fallbackUnit)

  return `({
    request: {
      url: ${usageUrlLiteral},
      method: "GET",
      headers: { "Authorization": "Bearer {{apiKey}}" }
    },
    extractor: function(response) {
      const remaining = response?.remaining ?? response?.quota?.remaining ?? response?.balance;
      const unit = response?.unit ?? response?.quota?.unit ?? ${fallbackUnitLiteral};
      return {
        isValid: response?.is_active ?? response?.isValid ?? true,
        remaining,
        unit
      };
    }
  })`
}

/**
 * withV1Endpoint removes trailing slashes and appends a missing v1 suffix.
 */
function withV1Endpoint(baseUrl: string): string {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '')
  return normalizedBaseUrl.endsWith('/v1') ? normalizedBaseUrl : `${normalizedBaseUrl}/v1`
}

/**
 * resolveCcSwitchImportConfig maps the selected client to its CCSwitch
 * application and endpoint form. Codex and Grok use a v1 suffix, other clients
 * use the root form, and model text is retained.
 */
export function resolveCcSwitchImportConfig(
  clientType: CcSwitchClientType,
  baseUrl: string,
  model: string,
): CcSwitchImportConfig {
  return {
    app: clientType === 'grok' ? 'grokbuild' : clientType,
    endpoint:
      clientType === 'grok' || clientType === 'codex'
        ? withV1Endpoint(baseUrl)
        : stripTrailingV1(baseUrl),
    model,
  }
}

/**
 * buildCcSwitchImportDeeplink serializes provider settings into a CCSwitch
 * custom-scheme URL. Its query includes the supplied API key, so callers
 * should treat the returned link as credential-bearing data.
 *
 * The usage script is encoded from UTF-8 bytes before base64 and query
 * escaping, preserving non-ASCII source across those representations. Client
 * endpoint normalization happens separately from the original homepage value.
 * A blank model is omitted from the query; other values are forwarded without
 * checking client installation or destination trust.
 */
export function buildCcSwitchImportDeeplink(input: CcSwitchImportDeeplinkInput): string {
  const config = resolveCcSwitchImportConfig(input.clientType, input.baseUrl, input.model)
  const entries: [string, string][] = [
    ['resource', 'provider'],
    ['app', config.app],
    ['name', input.providerName],
    ['homepage', input.baseUrl],
    ['endpoint', config.endpoint],
    ['apiKey', input.apiKey],
    ['configFormat', 'json'],
    ['usageEnabled', 'true'],
    ['usageScript', encodeBase64Utf8(input.usageScript)],
    ['usageAutoInterval', '30'],
  ]

  if (config.model) {
    entries.splice(2, 0, ['model', config.model])
  }

  return `ccswitch://v1/import?${new URLSearchParams(entries).toString()}`
}
