// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/thermometer.svg
import { createFallbackIcon } from '../createFallbackIcon'

// thermometer.ts supplies a thermometer outline to createFallbackIcon for the
// shared, single scale pulse on interaction. A single path joins the narrow
// stem to its rounded bulb, with the enclosing SVG supplying the line color and
// stroke thickness.

export default createFallbackIcon('thermometer', [
  [
    'path',
    {
      d: 'M14 4v10.54a4 4 0 1 1-4 0V4a2 2 0 0 1 4 0Z',
    },
  ],
])
