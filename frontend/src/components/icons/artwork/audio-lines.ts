// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/audio-lines.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// audio-lines.ts renders six vertical sound bars with independent changes in
// their lengths. The two outer bars stay still, while four inner paths use
// different durations to produce the changing waveform from one controller
// target.

// icon binds the audio-lines SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'audio-lines',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M2 10v3',
        },
        [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M6 6v11',
          variants: {
            normal: { d: 'M6 6v11' },
            animate: {
              d: ['M6 6v11', 'M6 10v3', 'M6 6v11'],
              transition: {
                duration: 1.5,
                repeat: 0,
              },
            },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M10 3v18',
          variants: {
            normal: { d: 'M10 3v18' },
            animate: {
              d: ['M10 3v18', 'M10 9v5', 'M10 3v18'],
              transition: {
                duration: 1,
                repeat: 0,
              },
            },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M14 8v7',
          variants: {
            normal: { d: 'M14 8v7' },
            animate: {
              d: ['M14 8v7', 'M14 6v11', 'M14 8v7'],
              transition: {
                duration: 0.8,
                repeat: 0,
              },
            },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M18 5v13',
          variants: {
            normal: { d: 'M18 5v13' },
            animate: {
              d: ['M18 5v13', 'M18 7v9', 'M18 5v13'],
              transition: {
                duration: 1.5,
                repeat: 0,
              },
            },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        'path',
        {
          d: 'M22 10v3',
        },
        [],
      ),
    ]),
}

export default icon
