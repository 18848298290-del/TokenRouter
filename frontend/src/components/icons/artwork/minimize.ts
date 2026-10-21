// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/minimize.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// minimize.ts renders four corners that move inward toward the icon center.
// Each corner uses the same spring settings with offsets matching its quadrant,
// and the normal target restores the four corners to their starting positions.

// DEFAULT_TRANSITION supplies the same spring response to all four corners as
// their quadrant-specific offsets move them toward or away from the center.
const DEFAULT_TRANSITION: Transition = {
  type: 'spring',
  stiffness: 250,
  damping: 25,
}

// icon binds the minimize SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'minimize',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M8 3v3a2 2 0 0 1-2 2H3',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateX: '0%', translateY: '0%' },
            animate: { translateX: '2px', translateY: '2px' },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M21 8h-3a2 2 0 0 1-2-2V3',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateX: '0%', translateY: '0%' },
            animate: { translateX: '-2px', translateY: '2px' },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M3 16h3a2 2 0 0 1 2 2v3',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateX: '0%', translateY: '0%' },
            animate: { translateX: '2px', translateY: '-2px' },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M16 21v-3a2 2 0 0 1 2-2h3',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateX: '0%', translateY: '0%' },
            animate: { translateX: '-2px', translateY: '-2px' },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
