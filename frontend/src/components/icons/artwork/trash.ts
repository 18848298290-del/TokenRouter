// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/trash.svg
import { createFallbackIcon } from '../createFallbackIcon'

// trash.ts supplies a trash bin to createFallbackIcon for the shared, single
// scale pulse on interaction. Separate paths define the lid, handle, body, and
// two interior ribs, keeping the bin's details together when the group scales.

export default createFallbackIcon('trash', [
  [
    'path',
    {
      d: 'M10 11v6',
    },
  ],
  [
    'path',
    {
      d: 'M14 11v6',
    },
  ],
  [
    'path',
    {
      d: 'M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6',
    },
  ],
  [
    'path',
    {
      d: 'M3 6h18',
    },
  ],
  [
    'path',
    {
      d: 'M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2',
    },
  ],
])
