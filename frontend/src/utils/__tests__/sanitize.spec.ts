import { describe, expect, it } from 'vitest'
import { sanitizeSvg } from '../sanitize'

/**
 * sanitize.spec.ts exercises the installed SVG sanitizer used by application icons.
 * The fixtures check that useful geometry, gradients, and filters remain renderable,
 * while executable elements, event attributes, and script URLs are removed from
 * supplied markup before consumers display it.
 */

describe('sanitizeSvg', () => {
  // Uploaded icons can combine geometry with local gradient and filter references.
  // Sanitization must retain those rendering inputs so dependency updates keep supported icons usable.
  it('retains SVG geometry, gradients, and filters', () => {
    const result = sanitizeSvg(`
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
        <defs>
          <linearGradient id="icon-gradient"><stop offset="0" stop-color="#2563eb" /></linearGradient>
          <filter id="icon-blur"><feGaussianBlur stdDeviation="1" /></filter>
        </defs>
        <rect width="24" height="24" fill="url(#icon-gradient)" filter="url(#icon-blur)" />
      </svg>
    `)
    const host = document.createElement('div')
    host.innerHTML = result

    expect(host.querySelector('svg')?.getAttribute('viewBox')).toBe('0 0 24 24')
    expect(host.querySelector('linearGradient')?.getAttribute('id')).toBe('icon-gradient')
    expect(host.querySelector('stop')?.getAttribute('stop-color')).toBe('#2563eb')
    expect(host.querySelector('feGaussianBlur')?.getAttribute('stdDeviation')).toBe('1')
    expect(host.querySelector('rect')?.getAttribute('fill')).toBe('url(#icon-gradient)')
    expect(host.querySelector('rect')?.getAttribute('filter')).toBe('url(#icon-blur)')
  })

  // Supplied SVG includes executable elements, event attributes, and a script link.
  // The returned markup must remove each executable surface while keeping the harmless shape.
  it('removes executable SVG content while retaining a safe shape', () => {
    const result = sanitizeSvg(`
      <svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)">
        <script>alert(1)</script>
        <foreignObject><div onclick="alert(1)">embedded HTML</div></foreignObject>
        <a href="javascript:alert(1)"><circle r="3" onclick="alert(1)" /></a>
      </svg>
    `)
    const host = document.createElement('div')
    host.innerHTML = result

    expect(host.querySelector('script')).toBeNull()
    expect(host.querySelector('foreignObject')).toBeNull()
    expect(host.querySelector('[onload], [onclick]')).toBeNull()
    expect(host.querySelector('a')?.getAttribute('href')).toBeNull()
    expect(host.querySelector('circle')?.getAttribute('r')).toBe('3')
  })
})
