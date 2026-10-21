import { describe, expect, it } from 'vitest'
import { buildCcSwitchImportDeeplink, buildCcSwitchUsageScript } from '@/utils/ccswitchImport'

/**
 * ccswitchImport.spec.ts checks the encoded settings exported to CCSwitch
 * clients. URL query decoding and UTF-8 script decoding expose the resulting
 * client, endpoint, model, and usage source so serialization errors cannot
 * hide behind a superficially valid custom-scheme link.
 */

/**
 * paramsFromDeeplink decodes the query portion of a generated import URL.
 * Links without a query produce an empty parameter collection, allowing
 * assertions to inspect absent fields without URL-scheme parsing.
 */
function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

/**
 * decodeBase64Utf8 decodes the exported script through base64 bytes and UTF-8
 * text.
 */
function decodeBase64Utf8(value: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(value), (char) => char.charCodeAt(0)))
}

describe('ccswitchImport utils', () => {
  /**
   * Base URLs include trailing slashes and nested API paths. Each generated
   * script must query the origin usage endpoint while retaining the API-key
   * substitution placeholder.
   */
  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/',
    'https://api.example.com/api/v1',
  ])('pins the usage script to /v1/usage for base URL %s', (baseUrl) => {
    const usageScript = buildCcSwitchUsageScript(baseUrl, 'USD')

    expect(usageScript).toContain('url: "https://api.example.com/v1/usage"')
    expect(usageScript).not.toContain('{{baseUrl}}')
    expect(usageScript).toContain('"Authorization": "Bearer {{apiKey}}"')
  })

  const baseInput = {
    baseUrl: 'https://api.example.com',
    providerName: 'TokenRouter',
    apiKey: 'sk-test',
    model: 'configured-model',
    usageScript: 'return true',
  }

  /**
   * A Codex import supplies a model and usage script. Decoded query parameters
   * must retain the model, API endpoint, and original script text for the
   * external client.
   */
  it('adds the Codex model parameter for OpenAI imports', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        clientType: 'codex',
      }),
    )

    expect(params.get('resource')).toBe('provider')
    expect(params.get('app')).toBe('codex')
    expect(params.get('endpoint')).toBe(`${baseInput.baseUrl}/v1`)
    expect(params.get('model')).toBe('configured-model')
    expect(decodeBase64Utf8(params.get('usageScript') || '')).toBe(baseInput.usageScript)
  })

  /**
   * Grok inputs vary their existing terminal v1 suffix and trailing slash. Every
   * generated import must select Grok Build and contain exactly one v1 endpoint
   * suffix.
   */
  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/',
  ])('imports Grok Build with one /v1 suffix for base URL %s', (baseUrl) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl,
        clientType: 'grok',
      }),
    )

    expect(params.get('app')).toBe('grokbuild')
    expect(params.get('endpoint')).toBe('https://api.example.com/v1')
    expect(params.get('model')).toBe('configured-model')
  })

  /**
   * Claude and Gemini imports share the selected model and root endpoint. Their
   * decoded application fields must match the chosen client without changing the
   * common settings.
   */
  it.each(['claude', 'gemini'] as const)(
    'imports %s with the selected model and common endpoint',
    (clientType) => {
      const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({ ...baseInput, clientType }))
      expect(params.get('app')).toBe(clientType)
      expect(params.get('endpoint')).toBe(baseInput.baseUrl)
      expect(params.get('model')).toBe('configured-model')
    },
  )

  /**
   * An Anthropic import includes a Chinese string in its usage script. The root
   * endpoint and decoded UTF-8 text must survive base64 and URL query
   * serialization.
   */
  it('uses the Anthropic root endpoint and preserves UTF-8 usage scripts', () => {
    const usageScript = 'return "余额"'
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl: 'https://api.example.com/v1',
        clientType: 'claude',
        usageScript,
      }),
    )

    expect(params.get('endpoint')).toBe('https://api.example.com')
    expect(decodeBase64Utf8(params.get('usageScript') || '')).toBe(usageScript)
  })
})
