/**
 * identicon.ts generates deterministic SVG avatar data URIs from normalized seed
 * strings. It hashes the trimmed, lowercased seed into a synchronous pseudorandom
 * sequence, uses that sequence for a seven-by-seven mirrored pattern, and chooses its
 * hue from the same sequence.
 */

/**
 * GRID_SIZE and HALF_COLUMNS define a seven-cell square with four sampled columns,
 * including its shared center column.
 */
const GRID_SIZE = 7
const HALF_COLUMNS = 4

/**
 * BACKGROUND_COLOR and the foreground saturation and lightness set the avatar palette,
 * while each seed selects its foreground hue.
 */
const BACKGROUND_COLOR = '#ececf0'
const FOREGROUND_SATURATION = 65
const FOREGROUND_LIGHTNESS = 50

/**
 * fnv1a hashes the seed's UTF-16 code units into an unsigned 32-bit value. Math.imul
 * retains 32-bit multiplication behavior, providing repeatable input for the
 * synchronous avatar generator.
 */
function fnv1a(input: string): number {
  let hash = 0x811c9dc5
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i)

    /**
     * Multiply by the FNV prime with 32-bit wrapping for repeatable patterns.
     */
    hash = Math.imul(hash, 0x01000193)
  }

  /**
   * Expose the hash as an unsigned generator seed.
   */
  return hash >>> 0
}

/**
 * mulberry32 creates a stateful pseudorandom generator from the unsigned 32-bit seed.
 * Each call advances its private state and returns a deterministic value in the
 * half-open interval from zero to one.
 */
function mulberry32(seed: number): () => number {
  // Truncate the seed to the generator's unsigned 32-bit state.
  let state = seed >>> 0
  return () => {
    // Wrap each advance before mixing its pseudorandom output bits.
    state = (state + 0x6d2b79f5) >>> 0
    let t = state
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    /**
     * Scale unsigned generator output into the half-open unit interval.
     */
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/**
 * identiconDataUri creates a percent-encoded SVG data URI for a mirrored avatar. Seeds
 * are trimmed and lowercased, empty seeds use a question mark, and identical normalized
 * seeds produce identical output.
 */
export function identiconDataUri(seed: string): string {
  const normalized = seed.trim().toLowerCase() || '?'
  const random = mulberry32(fnv1a(normalized))

  /**
   * Sample four columns including the center; the final three columns reuse those
   * sampled values to form a seven-column mirror.
   */
  const filled: boolean[][] = []
  for (let column = 0; column < HALF_COLUMNS; column++) {
    const columnCells: boolean[] = []
    for (let row = 0; row < GRID_SIZE; row++) {
      /**
       * Half of the random interval selects a filled cell.
       */
      columnCells.push(random() < 0.5)
    }
    filled.push(columnCells)
  }

  /**
   * Map the next random sample to whole hue degrees.
   */
  const hue = Math.floor(random() * 360)
  const foreground = `hsl(${hue}, ${FOREGROUND_SATURATION}%, ${FOREGROUND_LIGHTNESS}%)`

  const rects: string[] = [
    `<rect width="${GRID_SIZE}" height="${GRID_SIZE}" fill="${BACKGROUND_COLOR}"/>`,
  ]
  for (let column = 0; column < GRID_SIZE; column++) {
    /**
     * Mirror columns across the center using the previously sampled cells, so both
     * sides have identical occupancy for every avatar seed.
     */
    const sourceColumn = column < HALF_COLUMNS ? column : GRID_SIZE - 1 - column
    for (let row = 0; row < GRID_SIZE; row++) {
      if (filled[sourceColumn][row]) {
        rects.push(`<rect x="${column}" y="${row}" width="1" height="1" fill="${foreground}"/>`)
      }
    }
  }

  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${GRID_SIZE} ${GRID_SIZE}" shape-rendering="crispEdges">${rects.join('')}</svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}
