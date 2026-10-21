// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/braces.svg
import { createFallbackIcon } from '../createFallbackIcon'

// braces.ts supplies paired curly braces to createFallbackIcon for the shared,
// single scale pulse on interaction. The left and right outlines form an open
// center for the structured-output symbol, with both paths participating in one
// pulse.

export default createFallbackIcon('braces', [
  [
    'path',
    {
      d: 'M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5c0 1.1.9 2 2 2h1',
    },
  ],
  [
    'path',
    {
      d: 'M16 21h1a2 2 0 0 0 2-2v-5c0-1.1.9-2 2-2a2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1',
    },
  ],
])
