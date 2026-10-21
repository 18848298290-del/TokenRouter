import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import {
  calls,
  descendants,
  expression,
  initializer,
  loadProviderSource,
  printNode,
} from '../../__tests__/providerSource'

// CreateProviderModal.grok.spec.ts checks Grok creation controls and handler wiring.
// Parsed Vue attributes and named TypeScript handlers protect the API-key defaults,
// OAuth choices, and upstream configuration calls while allowing the component
// formatter to change whitespace and quote styles.

// component supplies parsed template controls and script declarations for all Grok structural assertions below.
const component = loadProviderSource('src/views/admin/providers/create/CreateProviderModal.vue')

describe('CreateProviderModal Grok provider types', () => {
  // Grok creation includes an API-key option with xAI defaults. The option keeps
  // its category assignment and both placeholder expressions return their documented Grok values.
  it('offers API-key setup alongside OAuth with the official xAI default', () => {
    const option = component.elements.find(
      (node) => node['data-testid'] === 'grok-provider-type-api-key',
    )
    expect(option?.['on:click']).toBe(expression("providerCategory = 'apikey'"))
    const grokDefault = descendants(component.script, ts.isConditionalExpression).find(
      (node) => expression(printNode(node.condition)) === expression("newPlatform === 'grok'"),
    )
    expect(grokDefault).toBeDefined()
    expect(ts.isStringLiteral(grokDefault!.whenTrue) && grokDefault!.whenTrue.text).toBe(
      'https://api.x.ai/v1',
    )
    for (const [name, expected] of [
      ['apiKeyBaseUrlPlaceholder', 'https://api.x.ai/v1'],
      ['apiKeyPlaceholder', 'xai-...'],
    ]) {
      const branch = descendants(initializer(component.script, name), ts.isCaseClause).find(
        (node) => ts.isStringLiteral(node.expression) && node.expression.text === 'grok',
      )
      expect(branch).toBeDefined()
      const returns = descendants(branch!, ts.isReturnStatement)
      expect(
        returns.some(
          (node) =>
            node.expression &&
            ts.isStringLiteral(node.expression) &&
            node.expression.text === expected,
        ),
      ).toBe(true)
    }
  })

  // OAuth creation exposes its optional relay controls under the Grok condition.
  // Attribute lookup protects the toggle and input independently of template line wrapping.
  it('exposes custom upstream URL and header override for the OAuth create flow', () => {
    expect(component.elements.some((node) => node.testid === 'grok-custom-base-url-toggle')).toBe(
      true,
    )
    expect(
      component.elements.some((node) => node['data-testid'] === 'grok-custom-base-url-input'),
    ).toBe(true)
    expect(
      component.elements.some(
        (node) => node['if:'] === expression("form.platform === 'grok' && isOAuthFlow"),
      ),
    ).toBe(true)
  })

  // Code exchange, refresh-token import, and SSO import each configure upstream access.
  // Checking every named handler catches a missing path even when other handlers contain duplicate calls.
  it.each(['handleGrokExchange', 'handleGrokValidateRT', 'handleGrokImportSSO'])(
    'validates and applies upstream config in %s',
    (name) => {
      const handlerCalls = calls(initializer(component.script, name))
      expect(handlerCalls).toContain('validateGrokOAuthUpstreamConfig')
      expect(handlerCalls).toContain('applyGrokOAuthUpstreamConfig')
    },
  )

  // Password authorization remains hidden in the OAuth component used during creation.
  // The positive component lookup prevents a missing flow from satisfying this visibility assertion.
  it('hides Grok password authorize option in the create flow', () => {
    const flow = component.elements.find((node) => node.tag === 'OAuthAuthorizationFlow')
    expect(flow).toBeDefined()
    expect(flow?.['bind:show-email-password-option']).toBe(expression('false'))
  })
})
