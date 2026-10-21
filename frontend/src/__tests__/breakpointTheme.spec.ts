import { describe, expect, it } from 'vitest'
import { parseVue } from './helpers/sourceSyntax'

import tailwindConfig from '../../tailwind.config.js'
import {
  BREAKPOINT_LG,
  BREAKPOINT_MD,
  BREAKPOINT_SM,
  MEDIA_MAX_SM,
  MEDIA_MIN_LG,
  MEDIA_MIN_MD,
  TABLE_DESKTOP_MEDIA_QUERY,
  TABLE_DESKTOP_MIN_WIDTH,
} from '../constants/layout'

// breakpointTheme.spec.ts compares layout constants with Tailwind's default
// responsive breakpoints and the custom-page drawer's CSS media query. Shared
// boundaries keep table pagination and page layouts switching at the same width,
// while maximum-width rules stay one pixel below their minimum-width counterpart.
describe('响应式断点契约', () => {
  // JavaScript layout consumers use the same three breakpoint values as Tailwind.
  // Keeping these constants explicit prevents responsive behavior from drifting across rendering paths.
  it('sm/md/lg 与 Tailwind 默认 screens 对齐(640/768/1024)', () => {
    expect(BREAKPOINT_SM).toBe(640)
    expect(BREAKPOINT_MD).toBe(768)
    expect(BREAKPOINT_LG).toBe(1024)
  })

  // Tailwind defaults define the stylesheet breakpoints used by the utility classes.
  // Neither the base theme nor its extension may override those shared screen values.
  it('tailwind.config.js 未自定义 screens(默认值即为契约)', () => {
    expect(tailwindConfig.theme.screens).toBeUndefined()
    expect(tailwindConfig.theme.extend?.screens).toBeUndefined()
  })

  // Maximum-width queries stop one pixel before the next minimum-width query begins.
  // The exported media strings therefore select mutually exclusive ranges at their boundaries.
  it('max 变体遵循 max = min - 1px 约定(互斥、无 1px 重叠带)', () => {
    expect(MEDIA_MAX_SM).toBe('(max-width: 639px)')
    expect(MEDIA_MIN_MD).toBe('(min-width: 768px)')
    expect(MEDIA_MIN_LG).toBe('(min-width: 1024px)')
  })

  // Tables and pagination switch to desktop behavior at the large breakpoint.
  // Their semantic aliases must continue to reference the same minimum width and media query.
  it('表格桌面断点是 lg 的语义别名(表格与分页器共用同一切换点)', () => {
    expect(TABLE_DESKTOP_MIN_WIDTH).toBe(BREAKPOINT_LG)
    expect(TABLE_DESKTOP_MEDIA_QUERY).toBe('(min-width: 1024px)')
  })

  // The custom-page drawer uses a literal CSS media query for its mobile range.
  // Parsing that rule checks the boundary even when the stylesheet formatter changes whitespace.
  it('CSS 侧无法用 var() 的媒体查询字面量与常量保持对齐', () => {
    const queries: string[] = []
    parseVue('views/user/CustomPageView.vue').styles.walkAtRules('media', (rule) => {
      queries.push(rule.params.replace(/\s/g, ''))
    })
    expect(queries).toContain(`(max-width:${BREAKPOINT_SM - 1}px)`)
    expect(queries).not.toContain('(max-width:640px)')
  })
})
