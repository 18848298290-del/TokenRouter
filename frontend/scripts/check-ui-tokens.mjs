#!/usr/bin/env node
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import {
  collectSourceFiles,
  exceptionRanges,
  frontendRoot,
  maskComments,
  parseSource,
  runSourceCheck,
  sourceLocation,
} from './source-analysis.mjs'

// check-ui-tokens.mjs checks frontend utility classes and style declarations
// against the shared UI tokens. Existing rules cover radii, font sizes, control
// dimensions, panel heights, layering, theme access, and viewport breakpoints.
// Tailwind configuration is checked separately because its values define which
// classes are available to every component in the application.
//
// A check-ui-allow comment exempts one parsed element opening tag, CSS declaration,
// or JavaScript statement. The source parser supplies ranges that remain stable
// in meaning after formatting. Comment examples are masked before matching, so
// documenting a prohibited class does not itself create a policy violation.

// RULES describes the prohibited source spellings and the corresponding shared
// token choices. Matching remains global so multiple violations receive separate diagnostics.
const RULES = [
  {
    id: 'legacy-radius',
    pattern:
      /(?<![\w-])rounded(?:-(?:t|b|l|r|tl|tr|bl|br|ss|se|es|ee))?-(?:sm|md|lg|xl|2xl|3xl|4xl)(?![\w-])/g,
    message: 'Use semantic rounded-compact/control/surface/dialog classes.',
  },
  {
    id: 'bare-rounded',
    pattern: /(?<![\w-])rounded(?![-\w])/g,
    message: 'Use rounded-compact for the default compact radius.',
  },
  {
    id: 'arbitrary-radius',
    pattern: /(?<![\w-])rounded(?:-(?:t|b|l|r|tl|tr|bl|br|ss|se|es|ee))?-\[/g,
    message: 'Use a semantic radius token, rounded-full, or rounded-none.',
  },
  {
    id: 'tiny-text',
    pattern: /text-\[(?:\d|1[01])(?:\.\d+)?px\]/g,
    message: 'Use text-xs or larger; the minimum font size is 12px.',
  },
  {
    id: 'btn-exact-h9',
    pattern:
      /class=(["'])(?=[^"']*(?<![\w-])h-9(?![\w-]))(?=[^"']*(?<![\w-])btn(?![\w-]))(?![^"']*(?<![\w-])btn-(?:sm|md|lg)(?![\w-]))[^"']*\1/g,
    message: 'Remove redundant h-9 from btn, or select an explicit btn size.',
  },
  {
    id: 'input-exact-h9',
    pattern:
      /class=(["'])(?=[^"']*(?<![\w-])h-9(?![\w-]))(?=[^"']*(?<![\w-])input(?![\w-]))[^"']*\1/g,
    message: 'Remove redundant h-9 from input; its baseline height is 36px.',
  },
  {
    id: 'manual-toggle-track',
    pattern:
      /class=(["'])(?=[^"']*(?<![\w-])h-6(?![\w-]))(?=[^"']*(?<![\w-])w-11(?![\w-]))[^"']*\1|class=(["'])(?=[^"']*(?<![\w-])h-5(?![\w-]))(?=[^"']*(?<![\w-])w-9(?![\w-]))[^"']*\2/g,
    message: 'Use Toggle for switch tracks and their supported sizes.',
  },
  {
    id: 'arbitrary-max-h',
    pattern: /(?<![\w-])max-h-\[\d+(?:\.\d+)?px\]/g,
    message: 'Use max-h-menu-sm/menu/panel, or document a local exception.',
  },
  {
    id: 'nonreactive-dark',
    pattern: /documentElement\s*\.\s*classList\s*\.\s*contains\s*\(\s*(['"])dark\1\s*,?\s*\)/g,
    message: 'Read reactive isDark from useTheme or useChartTheme.',
  },
  {
    id: 'arbitrary-z',
    pattern: /(?<![\w-])z-\[\d+\]/g,
    message: 'Use semantic z-index classes, or document a local stacking exception.',
  },
  {
    id: 'z-index-literal',
    pattern: /\bz-index\s*:\s*\d|\bzIndex\s*[:=]\s*['"]?\d/g,
    message: 'Use CSS z-index variables or Z_INDEX constants, or document a local exception.',
  },
  {
    id: 'literal-width-breakpoint',
    pattern:
      /(?:useMediaQuery|matchMedia)\(\s*['"]\((?:min|max)-width:\s*\d+px|\binnerWidth\s*[<>=]=?\s*\d{3,}|\b\d{3,}\s*[<]=\s*innerWidth\b/g,
    message: 'Use BREAKPOINT_* or MEDIA_* constants from constants/layout.ts.',
  },
]

// Radius declarations allow shared variables, square or circular radii, and CSS
// global values. Arbitrary numeric radii create values outside the shared scale.
const BORDER_RADIUS_DECL = /border-radius\s*:\s*([^;]+);/g
const ALLOWED_RADIUS_VALUE = /^([\s/]|0|9999px|inherit|initial|unset|revert|!important)*$/

/**
 * checkUI reports UI token violations within one parsed source file.
 * Comment text is excluded from matches, and each exception covers its selected
 * syntax unit. Callers receive original file positions for every remaining match.
 */
export function checkUI(text, filename) {
  const parsed = parseSource(text, filename)
  const allowed = exceptionRanges(text, parsed)
  const source = maskComments(text, parsed.comments)
  const violations = []
  for (const rule of RULES) {
    for (const match of source.matchAll(rule.pattern)) {
      if (allowed.some(([start, end]) => match.index >= start && match.index < end)) continue
      violations.push({
        ruleId: rule.id,
        ...sourceLocation(text, match.index),
        message: rule.message,
      })
    }
  }
  for (const match of source.matchAll(BORDER_RADIUS_DECL)) {
    const value = match[1].replace(/var\([^)]*\)/g, '').trim()
    if (ALLOWED_RADIUS_VALUE.test(value)) continue
    if (allowed.some(([start, end]) => match.index >= start && match.index < end)) continue
    violations.push({
      ruleId: 'raw-border-radius',
      ...sourceLocation(text, match.index),
      message: 'Use a radius variable, 0, 9999px, or a CSS global value.',
    })
  }
  return violations
}

/**
 * checkTailwindConfig checks the radius and panel-height token definitions.
 * Missing blocks create no diagnostics because Tailwind may inherit defaults;
 * present maxHeight entries must reference the shared CSS variable values.
 */
export function checkTailwindConfig(text) {
  const source = maskComments(text, parseSource(text, 'tailwind.config.js').comments)
  const violations = []
  const block = source.match(/borderRadius\s*:\s*\{([\s\S]*?)\}/)
  if (block) {
    const legacyKey = block[1].match(
      /(?:^|[\s,])(?:sm|md|lg|xl|DEFAULT|['"]?2xl['"]?|['"]?3xl['"]?|['"]?4xl['"]?)\s*:/,
    )
    if (legacyKey)
      violations.push({
        ruleId: 'legacy-radius-key',
        ...sourceLocation(text, block.index),
        message: 'Tailwind radius definitions must use semantic keys.',
      })
  }
  const maxHeight = source.match(/maxHeight\s*:\s*\{([\s\S]*?)\}/)
  if (maxHeight) {
    for (const pair of maxHeight[1].matchAll(/(['"]?[\w-]+['"]?)\s*:\s*([^,]+)/g)) {
      const value = pair[2].trim().replace(/^['"]|['"]$/g, '')
      if (value.startsWith('var(')) continue
      violations.push({
        ruleId: 'literal-max-height-key',
        ...sourceLocation(text, maxHeight.index),
        message: 'Tailwind maxHeight entries must reference shared CSS variables.',
      })
    }
  }
  return violations
}

/** runUI checks source tokens and the shared Tailwind definitions. */
function runUI() {
  const files = collectSourceFiles(join(frontendRoot, 'src')).filter(
    (file) => !file.startsWith(join(frontendRoot, 'src/i18n/')),
  )
  files.push(join(frontendRoot, 'index.html'))
  const sourceStatus = runSourceCheck('check:ui', checkUI, files)
  const configPath = join(frontendRoot, 'tailwind.config.js')
  const configIssues = checkTailwindConfig(readFileSync(configPath, 'utf8'))
  for (const issue of configIssues)
    console.error(`tailwind.config.js:${issue.line}:${issue.column} ${issue.message}`)
  return sourceStatus || (configIssues.length ? 1 : 0)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = runUI()
}
