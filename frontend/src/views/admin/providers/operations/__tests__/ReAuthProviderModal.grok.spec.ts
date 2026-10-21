import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import {
  calls,
  descendants,
  expression,
  initializer,
  loadProviderSource,
} from '@/views/admin/providers/__tests__/providerSource'

// ReAuthProviderModal.grok.spec.ts checks the Grok reauthorization controls
// and credential-update handlers through parsed Vue and TypeScript syntax.
// Named handler assertions protect each authentication route while allowing
// source formatting to change around bindings, function calls, and returned
// method names.

// component and flow identify the reauthorization source and its mounted
// authorization control for positive structural checks.
const component = loadProviderSource('src/views/admin/providers/operations/ReAuthProviderModal.vue')
const flow = component.elements.find((node) => node.tag === 'OAuthAuthorizationFlow')

describe('ReAuthProviderModal Grok re-auth paths', () => {
  // Reauthorization offers existing Grok SSO and refresh-token credentials to
  // the user. The authorization flow hides password input and has no
  // password-event binding to invoke.
  it('exposes SSO cookie and refresh-token options while hiding password auth', () => {
    expect(flow).toBeDefined()
    expect(flow?.['bind:show-sso-option']).toBe(expression('isGrok'))
    expect(flow?.['bind:show-email-password-option']).toBe(expression('false'))
    expect(flow?.['bind:show-refresh-token-option']).toBe(
      expression('isOpenAI || isAntigravity || isGrok'),
    )
    expect(flow?.['on:authorize-password']).toBeUndefined()
  })

  // Both Grok credential paths update the existing provider through the shared
  // merge API. Named call checks protect token conversion and reject a
  // batch-create call anywhere in this component.
  it('wires SSO and refresh-token reauth to the existing provider', () => {
    expect(flow?.['on:import-sso']).toBe(expression('handleGrokImportSSO'))
    expect(flow?.['on:validate-refresh-token']).toBe(expression('handleValidateRefreshToken'))
    expect(calls(initializer(component.script, 'handleValidateRefreshToken'))).toContain(
      'handleGrokValidateRefreshToken',
    )
    expect(calls(initializer(component.script, 'handleGrokImportSSO'))).toEqual(
      expect.arrayContaining(['grokOAuth.validateSSOToken', 'applyGrokReauthTokenInfo']),
    )
    expect(calls(initializer(component.script, 'applyGrokReauthTokenInfo'))).toEqual(
      expect.arrayContaining([
        'grokOAuth.buildCredentials',
        'adminAPI.providers.applyOAuthCredentials',
      ]),
    )
    expect(calls(component.script).some((name) => name.endsWith('.createFromSSO'))).toBe(false)
  })

  // SSO and refresh-token input complete through their own event handlers. The
  // manual-method expression contains both exclusions so the footer
  // code-exchange action stays hidden.
  it('hides footer code-exchange button for SSO and refresh-token input methods', () => {
    const exclusions = descendants(
      initializer(component.script, 'isManualInputMethod'),
      ts.isIfStatement,
    ).find((node) => {
      const methods = descendants(node.expression, ts.isBinaryExpression).flatMap((comparison) =>
        ts.isStringLiteral(comparison.right) ? [comparison.right.text] : [],
      )
      return methods.includes('sso_cookie') && methods.includes('refresh_token')
    })
    expect(exclusions).toBeDefined()
    expect(
      descendants(exclusions!.thenStatement, ts.isReturnStatement).some(
        (node) => node.expression?.kind === ts.SyntaxKind.FalseKeyword,
      ),
    ).toBe(true)
  })

  // Saved Grok refresh-token availability determines the initial
  // reauthorization method. The computed result offers SSO and refresh-token
  // methods while omitting password authorization as a returned choice.
  it('defaults reauth to refresh_token or sso_cookie', () => {
    expect(flow?.['bind:initial-input-method']).toBe(expression('grokInitialInputMethod'))
    const choices = descendants(
      initializer(component.script, 'grokInitialInputMethod'),
      ts.isReturnStatement,
    ).flatMap((node) =>
      node.expression && ts.isStringLiteral(node.expression) ? [node.expression.text] : [],
    )
    expect(choices).toEqual(expect.arrayContaining(['sso_cookie', 'refresh_token']))
    expect(choices).not.toContain('email_password')
    expect(
      descendants(component.script, ts.isVariableDeclaration).some(
        (node) => ts.isIdentifier(node.name) && node.name.text === 'grokPrefillEmailPassword',
      ),
    ).toBe(false)
  })
})
