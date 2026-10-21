import { describe, expect, it } from 'vitest'
import ts from 'typescript'
import {
  attribute,
  classes,
  directive,
  nodes,
  parseScript,
  parseVue,
} from '@/__tests__/helpers/sourceSyntax'

// RedeemView.layout.spec.ts verifies the redemption page's responsive grid and
// content placement through parsed elements. Desktop history owns a scrollable
// card, subscriptions occupy the second left-hand row, and the page presents
// redemption actions with their account history.

// Element ownership and parsed expressions identify the rendered regions while
// allowing source formatting to change independently of responsive layout rules.
const view = parseVue('views/user/RedeemView.vue')

// templateExpressions collects interpolation and directive expressions from the
// template ownership tree. Recursing through element children includes visible
// translation calls even when no directive is attached to their containing tag.
function templateExpressions(children: NonNullable<typeof view.template>['children']): string[] {
  return children.flatMap((child) => {
    if (child.type === 5 && child.content.type === 4) return [child.content.content]
    if (child.type !== 1) return []
    return [
      ...child.props.flatMap((prop) =>
        prop.type === 7 && prop.exp?.type === 4 ? [prop.exp.content] : [],
      ),
      ...templateExpressions(child.children),
    ]
  })
}

describe('RedeemView responsive layout', () => {
  // Desktop redemption fills the application viewport and gives history its own scroll area.
  // Pagination stays inside the history card while its rows use dividing lines.
  it('locks the desktop viewport and keeps history inside its own card', () => {
    const layout = view.elements.find((element) => element.tag === 'AppLayout')!
    expect(attribute(layout, 'fit-viewport')).toBe('')
    const grid = layout.children.find((child) => child.type === 1)!
    expect(grid.type).toBe(1)
    if (grid.type !== 1) throw new Error('Missing redemption grid')
    expect(classes(grid)).toEqual(
      expect.arrayContaining([
        'grid',
        'gap-4',
        'lg:min-h-0',
        'lg:flex-1',
        'lg:grid-cols-[22.5rem_minmax(0,1fr)]',
        'lg:grid-rows-[auto_minmax(0,1fr)]',
      ]),
    )
    const history = view.elements.find(
      (element) => attribute(element, 'data-testid') === 'redeem-history',
    )!
    expect(classes(history)).toEqual(
      expect.arrayContaining(['lg:col-start-2', 'lg:row-span-2', 'lg:row-start-1', 'lg:min-h-0']),
    )
    expect(
      history.children.some(
        (child) => child.type === 1 && classes(child).includes('lg:overflow-y-auto'),
      ),
    ).toBe(true)
    expect(history.children.some((child) => child.type === 1 && child.tag === 'Pagination')).toBe(
      true,
    )
    expect(view.elements.some((element) => classes(element).includes('mx-auto'))).toBe(false)
    const historyList = view.elements.find((element) => element.tag === 'ul')!
    const classValues = nodes(
      parseScript(directive(historyList, 'bind', 'class')!),
      ts.isStringLiteral,
    ).flatMap((node) => node.text.split(/\s+/))
    expect(classValues).toEqual(expect.arrayContaining(['divide-y', 'divide-gray-100']))
  })

  // Active subscriptions occupy the remaining left column below redemption on desktop.
  // The card delegates subscription rows to the same reusable list component used elsewhere.
  it('shows active subscriptions below the redeem panel on wide screens', () => {
    const subscriptions = view.elements.find(
      (element) => attribute(element, 'data-testid') === 'redeem-subscriptions',
    )!
    expect(classes(subscriptions)).toEqual(
      expect.arrayContaining(['lg:col-start-1', 'lg:row-start-2', 'lg:min-h-0']),
    )
    expect(
      subscriptions.children.some(
        (child) => child.type === 1 && child.tag === 'SubscriptionUsageList',
      ),
    ).toBe(true)
  })

  // Redemption guidance and contact settings are absent from the account workflow.
  // The template and script therefore contain no references that would restore those supplementary sections.
  it('omits supplementary redeem guidance', () => {
    const expressions = templateExpressions(view.template!.children)
    const literals = [
      ...nodes(view.script, ts.isStringLiteral).map((node) => node.text),
      ...expressions.flatMap((expression) =>
        nodes(parseScript(expression), ts.isStringLiteral).map((node) => node.text),
      ),
    ]
    expect(literals).not.toContain('redeem.redeemCodeHint')
    expect(literals).not.toContain('redeem.aboutCodes')
    const identifiers = nodes(view.script, ts.isIdentifier).map((node) => node.text)
    expect(identifiers).not.toContain('contactInfo')
    expect(identifiers).not.toContain('authAPI')
  })
})
