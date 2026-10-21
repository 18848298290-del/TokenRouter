import { identiconDataUri } from '../identicon'

/**
 * identicon.spec.ts tests deterministic seeded avatar data and SVG geometry.
 * Normalization and empty seeds receive dedicated cases, while a sample set
 * checks variation and parsed foreground cells verify the seven-column pattern
 * mirrors around its center.
 */

/**
 * decodeSvg removes the SVG data-URI prefix and decodes its markup. The test passes
 * generator output so assertions can inspect the view box and foreground rectangles
 * directly.
 */
function decodeSvg(dataUri: string): string {
  return decodeURIComponent(dataUri.replace(/^data:image\/svg\+xml,/, ''))
}

describe('identiconDataUri', () => {
  /**
   * The same email-like seed is passed to the avatar generator twice. Both calls return
   * identical data URIs, protecting deterministic rendering for a stable normalized
   * identity.
   */
  it('相同种子生成完全相同的头像', () => {
    expect(identiconDataUri('alice@example.com')).toBe(identiconDataUri('alice@example.com'))
  })

  /**
   * A seed differs only by surrounding spaces and letter case. Both forms produce the
   * same avatar URI after normalization, keeping cosmetic spelling changes from
   * changing identity display.
   */
  it('种子归一化：忽略大小写与首尾空格', () => {
    expect(identiconDataUri('  Alice@Example.COM ')).toBe(identiconDataUri('alice@example.com'))
  })

  /**
   * Empty and whitespace-only seeds cannot supply an avatar identity. Both resolve to
   * the same question-mark seed pattern, giving missing identities a deterministic
   * image.
   */
  it('空种子兜底为固定图案', () => {
    expect(identiconDataUri('')).toBe(identiconDataUri('?'))
    expect(identiconDataUri('   ')).toBe(identiconDataUri('?'))
  })

  /**
   * Five distinct sample seeds are rendered to avatar URIs. Their outputs are distinct
   * in this fixture set, checking useful variation without asserting universal
   * collision freedom.
   */
  it('不同种子生成互不相同的头像', () => {
    const seeds = [
      'alice@example.com',
      'bob@example.com',
      'carol@example.com',
      'dave@example.com',
      'erin@example.com',
    ]
    const outputs = new Set(seeds.map(identiconDataUri))
    expect(outputs.size).toBe(seeds.length)
  })

  /**
   * A generated avatar is decoded from its SVG data URI. Its markup declares a
   * seven-by-seven view box and crisp-edge rendering for the pixel grid.
   */
  it('输出为 7x7 网格的 SVG data URI', () => {
    const dataUri = identiconDataUri('alice@example.com')
    expect(dataUri.startsWith('data:image/svg+xml,')).toBe(true)
    const svg = decodeSvg(dataUri)
    expect(svg).toContain('viewBox="0 0 7 7"')
    expect(svg).toContain('shape-rendering="crispEdges"')
  })

  /**
   * Foreground cell coordinates are extracted from a generated avatar. Every occupied
   * cell has a counterpart mirrored across the seven-column grid, and the sampled
   * pattern contains foreground cells.
   */
  it('图案保持纵向镜像对称', () => {
    const svg = decodeSvg(identiconDataUri('alice@example.com'))

    const cells = new Set(
      [...svg.matchAll(/<rect x="(\d)" y="(\d)" width="1" height="1" fill="hsl\(/g)].map(
        (match) => `${match[1]},${match[2]}`,
      ),
    )
    expect(cells.size).toBeGreaterThan(0)
    for (const cell of cells) {
      const [x, y] = cell.split(',').map(Number)
      // Mirror across the center of the seven-column avatar grid.
      expect(cells.has(`${6 - x},${y}`)).toBe(true)
    }
  })
})
