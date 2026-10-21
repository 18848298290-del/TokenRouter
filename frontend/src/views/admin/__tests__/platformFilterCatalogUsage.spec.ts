import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { nodes, parseScript, parseVue } from '@/__tests__/helpers/sourceSyntax'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { buildOpsPlatformOptions } from '../ops/platformOptions'

// platformFilterCatalogUsage.spec.ts checks catalog ownership for administrator
// filters. General groups and subscriptions span provider platforms, while
// provider and error filters use the concrete platform catalog and operations
// prepend their translated all-platforms option.

describe('admin platform filters', () => {
  // Group and subscription management represent resources that span provider platforms.
  // Neither page reads a platform filter property that could silently narrow its requests.
  it('does not constrain groups or subscriptions to a platform', () => {
    for (const path of ['views/admin/GroupsView.vue', 'views/admin/SubscriptionsView.vue']) {
      const view = parseVue(path)
      const expressions = view.elements.flatMap((element) =>
        element.props.flatMap((prop) =>
          prop.type === 7 && prop.exp?.type === 4 ? [prop.exp.content] : [],
        ),
      )
      const accesses = [
        ...nodes(view.script, ts.isPropertyAccessExpression),
        ...expressions.flatMap((expression) =>
          nodes(parseScript(expression), ts.isPropertyAccessExpression),
        ),
      ]
      expect(
        accesses.some(
          (node) =>
            ts.isIdentifier(node.expression) &&
            node.expression.text === 'filters' &&
            node.name.text === 'platform',
        ),
      ).toBe(false)
    }
  })

  // Provider and error filters need the concrete provider catalog used by execution.
  // Each component imports the shared catalog and references it when constructing its choices.
  it('uses the concrete catalog for provider and error filters', () => {
    for (const path of [
      'views/admin/providers/table/ProviderTableFilters.vue',
      'views/admin/providers/operations/ErrorPassthroughRulesModal.vue',
    ]) {
      const script = parseVue(path).script
      const imports = nodes(script, ts.isImportDeclaration).filter(
        (node) =>
          ts.isStringLiteral(node.moduleSpecifier) &&
          node.moduleSpecifier.text === '@/constants/platforms',
      )
      expect(
        imports
          .flatMap((node) => nodes(node, ts.isImportSpecifier))
          .some((node) => node.name.text === 'CONCRETE_PLATFORM_OPTIONS'),
      ).toBe(true)
      expect(
        nodes(script, ts.isIdentifier).filter(
          (node) => node.text === 'CONCRETE_PLATFORM_OPTIONS' && !ts.isImportSpecifier(node.parent),
        ).length,
      ).toBeGreaterThan(0)
    }
  })

  // Operations filters prepend the translated all-platform choice to the concrete catalog.
  // The returned choices preserve catalog values and ordering for the actual provider platforms.
  it('builds operations choices from the shared catalog', () => {
    expect(buildOpsPlatformOptions('Every platform')).toEqual([
      { value: '', label: 'Every platform' },
      ...CONCRETE_PLATFORM_OPTIONS,
    ])
  })
})
