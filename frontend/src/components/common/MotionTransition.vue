<template>
  <Transition
    v-bind="$attrs"
    @before-enter="restoreEnteringElement"
    @before-leave="isolateLeavingElement"
  >
    <slot />
  </Transition>
</template>

<script setup lang="ts">
import { isolateLeavingElement, restoreEnteringElement } from '@/utils/leavingElement'

// MotionTransition.vue forwards transition attributes and events to Vue Transition
// while applying shared interaction isolation hooks. Entering elements regain their
// interaction state, and leaving elements are isolated before their visual exit
// completes.

defineOptions({ inheritAttrs: false })
</script>
