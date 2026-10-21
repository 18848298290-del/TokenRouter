// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/tags.svg
import { createFallbackIcon } from '../createFallbackIcon'

// tags.ts supplies overlapping price tags to createFallbackIcon for the shared,
// single scale pulse on interaction. The foreground tag contains a filled
// circular hole, and a second path provides the rear tag edge behind the main
// outline.

export default createFallbackIcon('tags', [
  [
    'path',
    {
      d: 'M13.172 2a2 2 0 0 1 1.414.586l6.71 6.71a2.4 2.4 0 0 1 0 3.408l-4.592 4.592a2.4 2.4 0 0 1-3.408 0l-6.71-6.71A2 2 0 0 1 6 9.172V3a1 1 0 0 1 1-1z',
    },
  ],
  [
    'path',
    {
      d: 'M2 7v6.172a2 2 0 0 0 .586 1.414l6.71 6.71a2.4 2.4 0 0 0 3.191.193',
    },
  ],
  [
    'circle',
    {
      cx: '10.5',
      cy: '6.5',
      r: '.5',
      fill: 'currentColor',
    },
  ],
])
