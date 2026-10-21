// index.ts exposes the shared Icon component and its accepted semantic names to
// frontend callers. The registry defines those names, and the component
// resolves their artwork while applying common sizing, accessibility, and
// interaction behavior.

export { default as Icon } from './Icon.vue'
export type { IconName } from './registry'
