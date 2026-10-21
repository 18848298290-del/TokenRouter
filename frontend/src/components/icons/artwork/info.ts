// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/info.svg
import { createFallbackIcon } from '../createFallbackIcon'

// info.ts supplies a circled information mark to createFallbackIcon for the
// shared, single scale pulse on interaction. The circle encloses a vertical
// stem and a short dot path, all using the color and stroke inherited from
// Icon.vue.

export default createFallbackIcon('info', [
  [
    'circle',
    {
      cx: '12',
      cy: '12',
      r: '10',
    },
  ],
  [
    'path',
    {
      d: 'M12 16v-4',
    },
  ],
  [
    'path',
    {
      d: 'M12 8h.01',
    },
  ],
])
