import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import postcss from 'postcss'
import tailwindcss from 'tailwindcss'
import theme from '../../tailwind.config.js'
import { cssRules, readSource } from './helpers/sourceSyntax'

// motionTheme.spec.ts builds the global stylesheet with an empty content scan.
// Vue creates transition classes at runtime, so their selectors must survive
// Tailwind's build even when no static template contains those names, including
// collapse geometry and reduced-motion rules used during those transitions.

describe('runtime transition styles', () => {
  // Runtime transitions receive class names that never appear in this content
  // fixture. Their entering and leaving rules must remain in the generated stylesheet.
  it('retains runtime transition rules without matching static markup', async () => {
    const path = resolve(__dirname, '../style.css')
    const result = await postcss([
      tailwindcss({ ...theme, content: [{ raw: '<div></div>' }] }),
    ]).process(readSource('style.css'), { from: path })
    for (const name of [
      'fade',
      'fade-slow',
      'dropdown-fade',
      'pop-float',
      'pop-fade',
      'modal',
      'collapse',
      'motion-list',
    ]) {
      for (const phase of ['enter-active', 'enter-from', 'leave-active', 'leave-to']) {
        expect(cssRules(result.root, `.${name}-${phase}`).length).toBeGreaterThan(0)
      }
    }
    const gridRows: string[] = []
    const values: string[] = []
    const mediaQueries: string[] = []
    result.root.walkDecls((node) => {
      values.push(node.value)
      if (node.prop === 'grid-template-rows') gridRows.push(node.value)
    })
    result.root.walkAtRules('media', (rule) => {
      mediaQueries.push(rule.params.replace(/\s/g, ''))
    })
    expect(gridRows).toContain('0fr')
    expect(values.some((value) => value.includes('var(--motion-layout)'))).toBe(true)
    expect(mediaQueries).toContain('(prefers-reduced-motion:reduce)')
  })
})
