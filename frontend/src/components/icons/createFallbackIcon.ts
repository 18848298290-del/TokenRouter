import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from './types'

// createFallbackIcon.ts turns Lucide SVG node tuples into artwork with a
// centered scale pulse. The returned definition delegates playback to
// useIconAnimation, allowing static source shapes to participate in the same
// hover, focus, and cancellation lifecycle.

// createFallbackIcon returns a definition that renders nodes inside one
// animated group. The renderer closes over the supplied node array, so callers
// keep that array and its attribute objects stable after construction.
export function createFallbackIcon(
  name: string,
  nodes: Array<[string, Record<string, string>]>,
): IconDefinition {
  return {
    name,
    normal: 'normal',
    animate: ['animate'],
    render: (controls) =>
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          style: { transformOrigin: '12px 12px' },
          // Scale keyframes return the complete shape to its resting size after
          // the single interaction pulse.
          variants: {
            normal: { scale: 1 },
            animate: { scale: [1, 1.06, 1] },
          },
          transition: { duration: 0.4, ease: 'easeInOut' },
        },
        () => nodes.map(([tag, attributes]) => h(tag, attributes)),
      ),
  }
}
