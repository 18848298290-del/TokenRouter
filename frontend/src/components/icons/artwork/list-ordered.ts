// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/list-ordered.svg
import { createFallbackIcon } from '../createFallbackIcon'

// list-ordered.ts supplies a numbered list to createFallbackIcon for the
// shared, single scale pulse on interaction. Three horizontal lines align with
// path-based numeral marks, keeping the list ordering visible alongside the
// longer row strokes.

export default createFallbackIcon('list-ordered', [
  [
    'path',
    {
      d: 'M11 5h10',
    },
  ],
  [
    'path',
    {
      d: 'M11 12h10',
    },
  ],
  [
    'path',
    {
      d: 'M11 19h10',
    },
  ],
  [
    'path',
    {
      d: 'M4 4h1v5',
    },
  ],
  [
    'path',
    {
      d: 'M4 9h2',
    },
  ],
  [
    'path',
    {
      d: 'M6.5 20H3.4c0-1 2.6-1.925 2.6-3.5a1.5 1.5 0 0 0-2.6-1.02',
    },
  ],
])
