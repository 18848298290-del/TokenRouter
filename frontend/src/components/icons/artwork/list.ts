// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/list.svg
import { createFallbackIcon } from '../createFallbackIcon'

// list.ts supplies a bulleted list to createFallbackIcon for the shared, single
// scale pulse on interaction. Three short dot strokes align with three longer
// horizontal lines, with the full set of rows sharing one animation group.

export default createFallbackIcon('list', [
  [
    'path',
    {
      d: 'M3 5h.01',
    },
  ],
  [
    'path',
    {
      d: 'M3 12h.01',
    },
  ],
  [
    'path',
    {
      d: 'M3 19h.01',
    },
  ],
  [
    'path',
    {
      d: 'M8 5h13',
    },
  ],
  [
    'path',
    {
      d: 'M8 12h13',
    },
  ],
  [
    'path',
    {
      d: 'M8 19h13',
    },
  ],
])
