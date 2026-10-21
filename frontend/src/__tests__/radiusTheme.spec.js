import { describe, expect, it } from 'vitest'
import postcss from 'postcss'
import tailwindConfig from '../../tailwind.config.js'
import { cssRules, declarations, readSource } from './helpers/sourceSyntax'

// radiusTheme.spec.js checks the relationship between Tailwind radius utilities
// and their CSS custom properties. Numeric radii belong to the root stylesheet,
// while configuration exposes semantic names and the explicit square and pill
// shapes that do not depend on those properties.

// rootRules retains every root declaration block so duplicate radius definitions are observable to the tests.
const rootRules = cssRules(postcss.parse(readSource('style.css')), ':root')

describe('radius theme', () => {
  // Semantic radii are configured through variable references, allowing the root
  // stylesheet to own their numeric values. All four named levels remain available.
  it('maps semantic radius utilities to CSS properties', () => {
    expect(tailwindConfig.theme.borderRadius).toMatchObject({
      compact: 'var(--radius-compact)',
      control: 'var(--radius-control)',
      surface: 'var(--radius-surface)',
      dialog: 'var(--radius-dialog)',
    })
  })

  // Each semantic property has exactly one value in the root stylesheet. This
  // guards the compact, control, surface, and dialog geometry against conflicting declarations.
  it.each([
    ['compact', '6px'],
    ['control', '8px'],
    ['surface', '12px'],
    ['dialog', '16px'],
  ])('defines %s exactly once', (name, value) => {
    expect(rootRules.flatMap((rule) => declarations(rule, `--radius-${name}`))).toEqual([value])
  })

  // Legacy scale keys must stay unavailable because the UI checks enforce
  // semantic naming. Accidentally restoring a scale would allow divergent visual choices.
  it('omits legacy scale keys', () => {
    for (const legacy of ['DEFAULT', 'sm', 'md', 'lg', 'xl', '2xl', '3xl', '4xl'])
      expect(tailwindConfig.theme.borderRadius).not.toHaveProperty(legacy)
  })

  // Square corners and fully rounded pills are explicit shape choices. Their
  // utilities remain available independently of the four semantic component radius levels.
  it('retains square and pill utilities', () => {
    expect(tailwindConfig.theme.borderRadius.none).toBe('0px')
    expect(tailwindConfig.theme.borderRadius.full).toBe('9999px')
  })
})
