import ts from 'typescript'
import { describe, expect, it, vi } from 'vitest'
import { nodes, parseVue } from '@/__tests__/helpers/sourceSyntax'
import viteConfig from '../../../../vite.config'

// stripeLazyLoading.spec.ts checks payment SDK loading and production chunk
// assignment. Payment pages dynamically request Stripe's pure loader, and the
// actual Vite configuration keeps Stripe and Airwallex in their dedicated chunks
// so shared application dependencies do not initiate payment SDK loading.

vi.mock('vite-plugin-checker', () => ({ default: () => ({ name: 'checker-test-stub' }) }))

// Payment consumers each own SDK startup; listing both protects the popup flow
// and the embedded payment page when their script formatting changes independently.
const stripeConsumers = ['views/user/StripePaymentView.vue', 'views/user/StripePopupView.vue']

describe('Stripe lazy loading', () => {
  // Each payment page requests the side-effect-free Stripe entry point dynamically.
  // Static SDK imports or the default entry point could load payment scripts before checkout.
  it.each(stripeConsumers)('%s uses the pure loader through dynamic import', (path) => {
    const script = parseVue(path).script
    const dynamicImports = nodes(script, ts.isCallExpression)
      .filter((node) => node.expression.kind === ts.SyntaxKind.ImportKeyword)
      .flatMap((node) => node.arguments.filter(ts.isStringLiteral).map((argument) => argument.text))
    expect(dynamicImports).toContain('@stripe/stripe-js/pure')
    expect(dynamicImports).not.toContain('@stripe/stripe-js')
    const staticImports = nodes(script, ts.isImportDeclaration)
      .filter((node) => !node.importClause?.isTypeOnly)
      .map((node) => (ts.isStringLiteral(node.moduleSpecifier) ? node.moduleSpecifier.text : ''))
    expect(staticImports.filter((name) => name.startsWith('@stripe/'))).toEqual([])
  })

  // Production chunk selection distinguishes payment SDKs from ordinary vendor dependencies.
  // Exercising the configured naming callback protects branch ordering and the shared vendor default.
  it('assigns payment SDKs to dedicated vendor chunks', async () => {
    const config = await viteConfig({ mode: 'production', command: 'build' })
    const output = config.build?.rolldownOptions?.output
    if (!output || Array.isArray(output))
      throw new Error('Expected one production output configuration')
    const group = output.codeSplitting?.groups?.[0]
    if (!group || typeof group.name !== 'function')
      throw new Error('Expected vendor chunk naming callback')
    expect(group.name('/node_modules/@stripe/stripe-js/dist/pure.js')).toBe('vendor-stripe')
    expect(group.name('/node_modules/@airwallex/components-sdk/index.js')).toBe('vendor-airwallex')
    expect(group.name('/node_modules/ordinary-library/index.js')).toBe('vendor-misc')
    expect(group.name('/src/views/user/StripePaymentView.vue')).toBeNull()
  })
})
