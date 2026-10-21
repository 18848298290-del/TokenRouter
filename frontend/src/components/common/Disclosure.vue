<template>
  <div>
    <button
      :id="`${id}-trigger`"
      type="button"
      :aria-expanded="open"
      :aria-controls="`${id}-content`"
      :class="summaryClass"
      @click="open = !open"
    >
      <Icon
        name="chevronRight"
        size="xs"
        class="inline-block shrink-0 transition-transform duration-normal"
        :class="{ 'rotate-90': open }"
        :animate-on-hover="false"
      />
      <slot name="summary" />
    </button>
    <Collapse :id="`${id}-content`" :open="open" :aria-labelledby="`${id}-trigger`">
      <slot />
    </Collapse>
  </div>
</template>

<script setup lang="ts">
import { ref, useId } from 'vue'
import Collapse from './Collapse.vue'
import Icon from '@/components/icons/Icon.vue'

// Disclosure.vue pairs a native disclosure button with a retained Collapse region. The
// component owns its initially closed state, and one generated identifier connects the
// expanded state, trigger, and content for assistive technology.

defineProps<{
  summaryClass?: string // Disclosure trigger class list.
}>()
// id links the button and content through ARIA attributes, while open owns this
// initially closed disclosure state.
const id = useId()
const open = ref(false)
</script>
