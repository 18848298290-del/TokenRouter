// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/circle.svg
import { createFallbackIcon } from '../createFallbackIcon'

// circle.ts supplies a circular outline to createFallbackIcon for the shared,
// single scale pulse on interaction. Its single circle is centered on the
// 24-unit canvas, and the unfilled interior inherits the outer SVG's drawing
// attributes.

export default createFallbackIcon('circle', [
  [
    'circle',
    {
      cx: '12',
      cy: '12',
      r: '10',
    },
  ],
])
