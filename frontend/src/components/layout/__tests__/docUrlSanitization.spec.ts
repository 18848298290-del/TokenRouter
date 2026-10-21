import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { sanitizeUrl } from '@/utils/url'
import { nodes, parseVue, variableInitializer } from '@/__tests__/helpers/sourceSyntax'

// docUrlSanitization.spec.ts checks document-link sanitization at each public
// entry point and verifies the URL policy with representative protocols. Script
// assertions inspect computed callback bodies so formatting cannot hide a
// missing sanitizer or satisfy the check through an unrelated call.

// entryPaths identifies the components whose document links consume publicly configured URLs directly.
const entryPaths = [
  'components/layout/AppHeader.vue',
  'views/HomeView.vue',
  'views/KeyUsageView.vue',
  'views/ModelMarketplaceView.vue',
]

describe('doc_url sanitization', () => {
  // Public entries compute document links from settings that may contain unsafe
  // protocols. Each computed getter must return the sanitizer result directly.
  it.each(entryPaths)('%s sanitizes its computed document URL', (path) => {
    const script = parseVue(path).script
    const imports = nodes(script, ts.isImportDeclaration)
    expect(
      imports.some(
        (node) =>
          ts.isStringLiteral(node.moduleSpecifier) &&
          node.moduleSpecifier.text === '@/utils/url' &&
          nodes(node, ts.isImportSpecifier).some((binding) => binding.name.text === 'sanitizeUrl'),
      ),
    ).toBe(true)
    const computed = variableInitializer(script, 'docUrl')
    expect(ts.isCallExpression(computed)).toBe(true)
    if (!ts.isCallExpression(computed)) throw new Error('docUrl must call computed')
    expect(computed.expression.getText(script)).toBe('computed')
    const getter = computed.arguments[0]
    expect(ts.isArrowFunction(getter)).toBe(true)
    if (!ts.isArrowFunction(getter) || !ts.isCallExpression(getter.body))
      throw new Error('Missing sanitizing getter')
    expect(getter.body.expression.getText(script)).toBe('sanitizeUrl')
    expect(getter.body.arguments).toHaveLength(1)
    expect(
      nodes(getter.body.arguments[0], ts.isPropertyAccessExpression).map((node) => node.name.text),
    ).toContain('docUrl')
  })

  // Configured document links can use executable or opaque protocols. The URL
  // policy rejects these inputs before a component can bind an anchor href.
  it('rejects executable and non-web protocols', () => {
    expect(sanitizeUrl('javascript:alert(1)')).toBe('')
    expect(sanitizeUrl('data:text/html,<script>alert(1)</script>')).toBe('')
    expect(sanitizeUrl('//evil.example/path')).toBe('')
  })

  // Administrators may configure either HTTP or HTTPS documentation hosts. Both
  // supported protocols remain usable, including URL normalization of host-only inputs.
  it('keeps valid HTTP and HTTPS document links', () => {
    expect(sanitizeUrl('https://docs.example.com/guide')).toBe('https://docs.example.com/guide')
    expect(sanitizeUrl('http://docs.example.com')).toBe('http://docs.example.com/')
  })
})
