// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/ticket.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// ticket.ts renders a ticket that separates at its perforation during
// interaction. The left body and perforation marks move as one group, while the
// right stub translates and rotates around an origin placed on the ticket's
// joining edge.

// TRANSITION shares spring settings across the tear; LEFT_VARIANTS moves the
// main body and RIGHT_VARIANTS moves and tilts the stub around its joining
// edge.
const TRANSITION: Transition = {
  type: 'spring',
  stiffness: 300,
  damping: 20,
}

const LEFT_VARIANTS: Variants = {
  normal: { x: 0 },
  animate: { x: -3 },
}

const RIGHT_VARIANTS: Variants = {
  normal: { x: 0, rotate: 0 },
  animate: { x: 3, rotate: 4 },
}

// icon binds the ticket SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'ticket',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          transition: TRANSITION,
          variants: LEFT_VARIANTS,
        },
        () => [
          h(
            'path',
            {
              d: 'M13 5H4a2 2 0 0 0-2 2v2a3 3 0 0 1 0 6v2a2 2 0 0 0 2 2h9',
            },
            [],
          ),
          h(
            'path',
            {
              d: 'M13 5v2',
            },
            [],
          ),
          h(
            'path',
            {
              d: 'M13 11v2',
            },
            [],
          ),
          h(
            'path',
            {
              d: 'M13 17v2',
            },
            [],
          ),
        ],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M13 5h7a2 2 0 0 1 2 2v2a3 3 0 0 0 0 6v2a2 2 0 0 1-2 2h-7',
          initial: 'normal',
          style: { transformOrigin: '13px 12px' },
          transition: TRANSITION,
          variants: RIGHT_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
