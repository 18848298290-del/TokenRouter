import type { VNodeChild } from 'vue'
import type { MotionProps, useAnimationControls } from 'motion-v'

// types.ts defines the artwork interface shared by the registry, SVG wrapper,
// and animation lifecycle. Artwork supplies render nodes and ordered playback
// targets, while useIconAnimation owns interaction state and the controls that
// drive those nodes.

// IconControls is the motion-v controller shared by one rendered icon instance.
export type IconControls = ReturnType<typeof useAnimationControls>
// IconAnimation describes one target accepted by the controller start
// operation.
export type IconAnimation = Parameters<IconControls['start']>[0]
// IconVariants maps motion group targets to their SVG animation definitions.
export type IconVariants = NonNullable<MotionProps<'g'>['variants']>

// IconDefinition supplies an artwork identity, resting target, playback
// sequence, and SVG renderer.
export interface IconDefinition {
  name: string // artwork diagnostic name
  // normal names the resting variant applied when interaction ends or animation
  // becomes suppressed.
  normal: string
  // animate lists targets in playback order; useIconAnimation awaits each
  // target before starting its successor.
  animate: IconAnimation[]
  // render creates artwork nodes using the instance controls and caller stroke
  // width. Icon.vue mounts these nodes inside its root SVG, so renderers
  // inherit root drawing attributes.
  render: (controls: IconControls, strokeWidth: number) => VNodeChild
}
