// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/palette.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// palette.ts renders a palette whose outline draws before its colored wells
// appear. The outline duration also delays the child circle animations, and
// per-child staggering reveals the filled dots after the enclosing palette
// shape has been drawn.

// DASH_LENGTH sets the outline dash length in SVG units; DRAW_DURATION and
// DOT_STAGGER express seconds for the outline and well entrances, with DOTS and
// the variants connecting those phases.
const DASH_LENGTH = 70

const DRAW_DURATION = 0.45

const DOT_STAGGER = 0.08

const DOTS = [
  { cx: 6.5, cy: 12.5 },
  { cx: 8.5, cy: 7.5 },
  { cx: 13.5, cy: 6.5 },
  { cx: 17.5, cy: 10.5 },
]

const OUTLINE_VARIANTS: Variants = {
  normal: {
    strokeDashoffset: 0,
  },
  animate: {
    strokeDashoffset: [DASH_LENGTH, 0],
    transition: {
      duration: DRAW_DURATION,
      ease: [0.65, 0, 0.35, 1],
    },
  },
}

const DOTS_GROUP_VARIANTS: Variants = {
  normal: {},
  animate: {
    transition: {
      delayChildren: DRAW_DURATION,
      staggerChildren: DOT_STAGGER,
    },
  },
}

const DOT_VARIANTS: Variants = {
  normal: {
    scale: 1,
    transition: { duration: 0.2 },
  },
  animate: {
    scale: [0, 1],
    transition: {
      damping: 10,
      stiffness: 300,
      type: 'spring',
    },
  },
}

// icon binds the palette SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'palette',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M12 2a1 1 0 0 0 0 20l.25 0a1.75 1.75 0 0 0 1.4-2.8l-.3-.4a1.75 1.75 0 0 1 1.4-2.8h2.25a5 5 0 0 0 5-5 10 9 0 0 0-10-9z',
          initial: 'normal',
          strokeDasharray: DASH_LENGTH,
          variants: OUTLINE_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          variants: DOTS_GROUP_VARIANTS,
        },
        () => [
          DOTS.map((dot) =>
            h(
              motion.circle,
              {
                cx: dot.cx,
                cy: dot.cy,
                fill: 'currentColor',
                key: `${dot.cx}-${dot.cy}`,
                r: '.5',
                style: { transformBox: 'fill-box', transformOrigin: 'center' },
                variants: DOT_VARIANTS,
                initial: 'normal',
              },
              () => [],
            ),
          ),
        ],
      ),
    ]),
}

export default icon
