// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/circle-alert.svg
import { createFallbackIcon } from '../createFallbackIcon'

// circle-alert.ts supplies a circled exclamation mark to createFallbackIcon for
// the shared, single scale pulse on interaction. The circular outline encloses
// a vertical line and short dot stroke, keeping the warning mark centered
// within the surrounding ring.

export default createFallbackIcon('circle-alert', [
  [
    'circle',
    {
      cx: '12',
      cy: '12',
      r: '10',
    },
  ],
  [
    'line',
    {
      x1: '12',
      x2: '12',
      y1: '8',
      y2: '12',
    },
  ],
  [
    'line',
    {
      x1: '12',
      x2: '12.01',
      y1: '16',
      y2: '16',
    },
  ],
])
