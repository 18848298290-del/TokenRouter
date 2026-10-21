import postcss from 'postcss'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import postcssConfig from '../../postcss.config.js'
import tailwindConfig from '../../tailwind.config.js'
import { applyTokens, cssRules, nodes, parseScript, readSource } from './helpers/sourceSyntax'

// fontTheme.spec.js checks font loading order, fallback families, and Chinese
// font-face processing. Parsed import declarations and CSS rules protect these
// choices independently of source formatting, while the actual PostCSS plugin
// verifies the transformed weight range and Unicode coverage of generated faces.

// fontFamily holds the configured fallback stacks used by the site's text and code utilities.
const fontFamily = tailwindConfig.theme.extend.fontFamily

// fontFace creates one minimal font-face fixture for the supplied Unicode coverage.
function fontFace(unicodeRange) {
  return `@font-face{font-family:x;font-weight:100 900;src:url(a.woff2);unicode-range:${unicodeRange}}`
}

// processFont applies the configured Chinese-font adjustment to a supplied stylesheet fixture.
function processFont(css, from) {
  return postcss([postcssConfig.plugins[0]]).process(css, { from })
}

// propertyValues collects matching property values from generated font-face declarations in traversal order.
function propertyValues(root, property) {
  const values = []
  root.walkDecls(property, (node) => {
    values.push(node.value)
  })
  return values
}

describe('font theme', () => {
  // Latin text uses Jakarta while Chinese text first prefers the local PingFang
  // family. Noto Sans and common platform fonts remain available as additional fallbacks.
  it('orders Latin and Chinese sans-serif fallbacks', () => {
    expect(fontFamily.sans.slice(0, 3)).toEqual([
      '"Plus Jakarta Sans Variable"',
      '"PingFang SC"',
      '"Noto Sans SC Variable"',
    ])
    expect(fontFamily.sans).toEqual(
      expect.arrayContaining(['Hiragino Sans GB', 'Microsoft YaHei', 'sans-serif']),
    )
  })

  // Monospaced content uses Geist Mono first and then platform code fonts.
  // The generic monospace fallback remains present for systems lacking the named families.
  it('retains Geist Mono and platform monospace fallbacks', () => {
    expect(fontFamily.mono[0]).toBe('"Geist Mono Variable"')
    expect(fontFamily.mono).toEqual(
      expect.arrayContaining([
        'ui-monospace',
        'SFMono-Regular',
        'Menlo',
        'Monaco',
        'Consolas',
        'monospace',
      ]),
    )
  })

  // Font faces must load before the global stylesheet declares the text theme.
  // Import syntax nodes preserve execution order across quote changes and wrapped declarations.
  it('loads font faces before the global stylesheet', () => {
    const imports = nodes(parseScript(readSource('main.ts')), ts.isImportDeclaration).map(
      (node) => node.moduleSpecifier.text,
    )
    const styleIndex = imports.indexOf('./style.css')
    expect(styleIndex).toBeGreaterThanOrEqual(0)
    for (const font of [
      '@fontsource-variable/plus-jakarta-sans',
      '@fontsource-variable/plus-jakarta-sans/wght-italic.css',
      '@fontsource-variable/geist-mono',
      '@fontsource-variable/noto-sans-sc',
    ]) {
      expect(imports.indexOf(font)).toBeGreaterThanOrEqual(0)
      expect(imports.indexOf(font)).toBeLessThan(styleIndex)
    }
  })

  // Chinese font slices cap weights and hand emoji codepoints to system fonts.
  // Mixed slices retain text glyphs, emoji-only slices disappear, and other families stay untouched.
  it('caps Chinese weights and removes emoji coverage', async () => {
    const notoCss = '/repo/node_modules/@fontsource-variable/noto-sans-sc/index.css'
    // U+2605 remains a text glyph, while the food and checkmark codepoints must leave the Chinese font slice.
    const mixed = await processFont(fontFace('U+4e00-4e02,U+1f364-1f366,U+2605,U+2705'), notoCss)
    expect(propertyValues(mixed.root, 'font-weight')).toEqual(['100 600'])
    expect(propertyValues(mixed.root, 'unicode-range')).toEqual(['U+4e00-4e02,U+2605'])
    const emojiOnly = await processFont(fontFace('U+1f364-1f366'), notoCss)
    expect(emojiOnly.root.nodes).toEqual([])
    const jakarta = await processFont(
      fontFace('U+1f365'),
      '/repo/node_modules/@fontsource-variable/plus-jakarta-sans/index.css',
    )
    expect(propertyValues(jakarta.root, 'font-weight')).toEqual(['100 900'])
    expect(propertyValues(jakarta.root, 'unicode-range')).toEqual(['U+1f365'])
  })

  // The root document establishes the sans-serif theme and onboarding inherits
  // its font. No onboarding rule should replace that stack with a local system stack.
  it('inherits the shared theme in the document and onboarding overlay', () => {
    expect(cssRules(postcss.parse(readSource('style.css')), 'html').flatMap(applyTokens)).toContain(
      'font-sans',
    )
    const onboarding = postcss.parse(readSource('styles/onboarding.css'))
    const fontDeclarations = []
    onboarding.walkDecls('font-family', (node) => {
      fontDeclarations.push(node)
    })
    expect(fontDeclarations.some((node) => node.value === 'inherit' && node.important)).toBe(true)
    expect(fontDeclarations.some((node) => node.value.startsWith('ui-sans-serif'))).toBe(false)
  })
})
