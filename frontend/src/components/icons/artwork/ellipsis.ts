// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/ellipsis.svg
import { createFallbackIcon } from '../createFallbackIcon'

// ellipsis.ts supplies three horizontal dots to createFallbackIcon for the
// shared, single scale pulse on interaction. The circles share one vertical
// coordinate and equal radii, with their spacing retained when the shared
// animation scales the group.

export default createFallbackIcon('ellipsis', [
  [
    'circle',
    {
      cx: '12',
      cy: '12',
      r: '1',
    },
  ],
  [
    'circle',
    {
      cx: '19',
      cy: '12',
      r: '1',
    },
  ],
  [
    'circle',
    {
      cx: '5',
      cy: '12',
      r: '1',
    },
  ],
])
