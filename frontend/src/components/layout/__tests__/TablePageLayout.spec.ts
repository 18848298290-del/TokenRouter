import { mount, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import postcss from 'postcss'
import {
  applyTokens,
  cssRules,
  declarations,
  parseVue,
  readSource,
} from '@/__tests__/helpers/sourceSyntax'
import TablePageLayout from '../TablePageLayout.vue'

// TablePageLayout.spec.ts verifies table framing and responsive scroll ownership.
// Mounted components cover slots and resize handling, while parsed stylesheet
// rules cover Tailwind utilities and overscroll declarations that jsdom cannot
// evaluate as browser layout or computed responsive styles.

// Parsed styles bind each assertion to its owning selector and retain duplicate declarations for inspection.
const styles = parseVue('components/layout/TablePageLayout.vue').styles
const globalStyles = postcss.parse(readSource('style.css'))
enableAutoUnmount(afterEach)

describe('TablePageLayout responsive table scrolling', () => {
  // Pagination shares the same frame as table content so desktop scrolling
  // leaves its footer attached. Both slots must render inside that shared frame.
  it('renders pagination inside the shared table frame', () => {
    const wrapper = mount(TablePageLayout, {
      slots: {
        table: '<div data-test="table-content">table</div>',
        pagination: '<div data-test="pagination-content">pagination</div>',
      },
    })
    const frame = wrapper.get('.table-scroll-container')
    expect(frame.get('[data-test="table-content"]').exists()).toBe(true)
    expect(frame.get('.table-pagination-footer [data-test="pagination-content"]').exists()).toBe(
      true,
    )
    const overrides: string[] = []
    styles.walkDecls('--pagination-control-height', (node) => {
      overrides.push(node.value)
    })
    expect(overrides).toEqual([])
  })

  // The parent flex chain allocates table height at desktop widths. Component
  // declarations must avoid mirrored page-padding variables and viewport subtraction formulas.
  it('takes its height from the parent flex chain', () => {
    expect(cssRules(styles, '.table-page-layout').flatMap(applyTokens)).toEqual(
      expect.arrayContaining(['flex', 'flex-1', 'flex-col', 'min-h-0']),
    )
    styles.walkDecls((node) => {
      expect([node.prop, node.value].join(' ')).not.toMatch(
        /--main-pad-(top|bottom)|--page-heading-space|calc\(100d?vh/,
      )
    })
  })

  // Sticky table headers cover body rows while scrolling. Their owning rule
  // uses opaque light and dark backgrounds and does not introduce backdrop blur.
  it('keeps shared sticky table headers opaque', () => {
    expect(cssRules(styles, '.table-scroll-container :deep(thead)').flatMap(applyTokens)).toEqual(
      expect.arrayContaining(['bg-gray-50', 'dark:bg-dark-900']),
    )
    styles.walkAtRules('apply', (rule) => {
      expect(rule.params.split(/\s+/)).not.toContain('backdrop-blur-sm')
    })
  })

  // A resize across the desktop breakpoint changes the mounted layout mode.
  // The listener must apply the mobile class below 1024px and remove it above.
  it('switches from mobile to desktop at 1024px', async () => {
    const originalWidth = window.innerWidth
    let wrapper: ReturnType<typeof mount> | undefined
    try {
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: 840 })
      wrapper = mount(TablePageLayout, { slots: { table: '<div>table</div>' } })
      await wrapper.vm.$nextTick()
      expect(wrapper.get('.table-page-layout').classes()).toContain('mobile-mode')
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1024 })
      window.dispatchEvent(new Event('resize'))
      await wrapper.vm.$nextTick()
      expect(wrapper.get('.table-page-layout').classes()).not.toContain('mobile-mode')
    } finally {
      // Restore the shared viewport after removing this component's listener so later tests observe their own fixture dimensions.
      wrapper?.unmount()
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth })
    }
  })

  // Mobile cards can overflow vertically while their table wrapper still owns
  // horizontal scrolling. Mobile-specific wrapper rules must not disable that scroll container.
  it('retains horizontal scrolling in mobile mode', () => {
    expect(
      cssRules(styles, '.table-scroll-container :deep(.table-wrapper)').flatMap(applyTokens),
    ).toContain('overflow-x-auto')
    styles.walkRules((rule) => {
      if (
        rule.selector.includes('.mobile-mode') &&
        rule.selector.includes(':deep(.table-wrapper)')
      ) {
        expect(applyTokens(rule)).not.toContain('overflow-visible')
        expect(declarations(rule, 'overflow')).not.toContain('visible')
      }
    })
  })

  // Reaching a table or scroll container's vertical edge must continue scrolling
  // the outer page. Each shared selector suppresses horizontal boundary effects
  // while allowing vertical chaining.
  it('allows vertical scrolling to chain to the outer page', () => {
    for (const selector of [
      'table',
      ':where(.table-wrapper, .table-container, .table-scroll-container)',
      ':where(.overflow-auto, .overflow-x-auto, .overflow-y-auto):has(table)',
    ]) {
      const rules = cssRules(globalStyles, selector)
      expect(rules.flatMap((rule) => declarations(rule, 'overscroll-behavior-x'))).toEqual(['none'])
      expect(rules.flatMap((rule) => declarations(rule, 'overscroll-behavior-y'))).toEqual(['auto'])
      expect(rules.flatMap((rule) => declarations(rule, 'overscroll-behavior'))).not.toContain(
        'none',
      )
    }
  })
})
