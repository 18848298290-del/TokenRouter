import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { nodes, parseVue, variableInitializer } from '@/__tests__/helpers/sourceSyntax'

// siteLogoSanitization.spec.ts checks sanitizer inputs and options for site
// branding and favicon consumers. Syntax assertions attach the allowed relative
// and data URL options to the actual logo sanitizer call, so unrelated options
// elsewhere in a component cannot satisfy the policy check.

// entries maps branding consumers to their explicit cached-settings fallback requirement for logo inputs.
const entries = [
  ['components/layout/AppHeader.vue', false],
  ['views/HomeView.vue', true],
  ['views/KeyUsageView.vue', true],
  ['views/ModelMarketplaceView.vue', true],
  ['App.vue', false],
] as const

describe('site_logo sanitization', () => {
  // Branding accepts local assets and data images while sanitizing configured
  // URLs. Each consumer must pass these permissions on its own sanitizer call.
  it.each(entries)('%s sanitizes its logo with the required options', (path, cached) => {
    const script = parseVue(path).script
    const scope = path === 'App.vue' ? script : variableInitializer(script, 'siteLogo')
    const calls = nodes(scope, ts.isCallExpression).filter(
      (node) => ts.isIdentifier(node.expression) && node.expression.text === 'sanitizeUrl',
    )
    const call = calls.find(
      (node) =>
        node.arguments[0] &&
        nodes(node.arguments[0], ts.isPropertyAccessExpression).some(
          (property) => property.name.text === 'siteLogo',
        ),
    )
    expect(call).toBeDefined()
    if (!call) throw new Error('Missing logo sanitizer call')
    const input = call.arguments[0]
    expect(ts.isBinaryExpression(input)).toBe(true)
    if (!ts.isBinaryExpression(input)) throw new Error('Missing empty logo fallback')
    expect(input.operatorToken.kind).toBe(ts.SyntaxKind.BarBarToken)
    expect(ts.isStringLiteral(input.right) && input.right.text).toBe('')
    let storeLogo = input.left
    if (cached) {
      expect(ts.isBinaryExpression(storeLogo)).toBe(true)
      if (!ts.isBinaryExpression(storeLogo)) throw new Error('Missing cached logo precedence')
      expect(storeLogo.operatorToken.kind).toBe(ts.SyntaxKind.BarBarToken)
      const cachedLogo = storeLogo.left
      expect(ts.isPropertyAccessExpression(cachedLogo)).toBe(true)
      if (!ts.isPropertyAccessExpression(cachedLogo))
        throw new Error('Missing cached logo property')
      expect(cachedLogo.name.text).toBe('site_logo')
      expect(cachedLogo.questionDotToken).toBeDefined()
      expect(
        ts.isPropertyAccessExpression(cachedLogo.expression) && cachedLogo.expression.name.text,
      ).toBe('cachedPublicSettings')
      storeLogo = storeLogo.right
    }
    expect(ts.isPropertyAccessExpression(storeLogo)).toBe(true)
    if (!ts.isPropertyAccessExpression(storeLogo)) throw new Error('Missing store logo property')
    expect(storeLogo.name.text).toBe('siteLogo')
    expect(ts.isIdentifier(storeLogo.expression) && storeLogo.expression.text).toBe('appStore')
    const options = call.arguments[1]
    expect(ts.isObjectLiteralExpression(options)).toBe(true)
    if (!ts.isObjectLiteralExpression(options)) throw new Error('Missing logo sanitizer options')
    const values = Object.fromEntries(
      options.properties
        .filter(ts.isPropertyAssignment)
        .map((node) => [
          ts.isIdentifier(node.name) || ts.isStringLiteral(node.name) ? node.name.text : '',
          node.initializer.kind,
        ]),
    )
    expect(values).toMatchObject({
      allowRelative: ts.SyntaxKind.TrueKeyword,
      allowDataUrl: ts.SyntaxKind.TrueKeyword,
    })
    expect(
      nodes(script, ts.isImportDeclaration).some(
        (node) =>
          ts.isStringLiteral(node.moduleSpecifier) &&
          node.moduleSpecifier.text === '@/utils/url' &&
          nodes(node, ts.isImportSpecifier).some((binding) => binding.name.text === 'sanitizeUrl'),
      ),
    ).toBe(true)
  })
})
