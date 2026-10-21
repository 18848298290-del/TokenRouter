// Artwork from Lucide, adapted with the shared scale pulse.
// See ../README.md for the source license and pinned version.
// https://github.com/lucide-icons/lucide/blob/66d8f9fc394b8530377e5f6112f0b8908ba01280/icons/folder.svg
import { createFallbackIcon } from '../createFallbackIcon'

// folder.ts supplies a folder outline to createFallbackIcon for the shared,
// single scale pulse on interaction. One continuous path describes the tab and
// body, and the wrapper supplies the stroke and fill behavior for the entire
// shape.

export default createFallbackIcon('folder', [
  [
    'path',
    {
      d: 'M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z',
    },
  ],
])
