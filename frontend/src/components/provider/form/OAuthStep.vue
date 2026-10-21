<template>
  <li class="relative flex gap-4" :class="!last && 'pb-6'">
    <!-- Vertical connector between authorization steps. -->
    <span
      v-if="!last"
      aria-hidden="true"
      class="absolute bottom-0 left-3.5 top-8 w-px bg-gray-200 dark:bg-dark-600"
    />
    <span
      class="relative flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold transition-colors"
      :class="
        done
          ? 'bg-primary-600 text-white'
          : 'border border-gray-300 bg-white text-gray-600 dark:border-dark-500 dark:bg-dark-900 dark:text-gray-300'
      "
    >
      <Icon v-if="done" name="check" size="xs" :stroke-width="2.5" :animate-on-hover="false" />
      <template v-else>{{ index }}</template>
    </span>
    <div class="min-w-0 flex-1 space-y-3 pt-1">
      <div>
        <p class="text-sm font-medium text-primary-900 dark:text-dark-50">{{ title }}</p>
        <p v-if="description" class="input-hint">{{ description }}</p>
      </div>
      <slot />
    </div>
  </li>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'

// OAuthStep.vue renders one numbered step of the manual authorization flow. The caller
// supplies its title, explanatory content, and completion state, while the last-step flag
// removes the connecting line and the default slot contains the actual step controls.
defineProps<{
  index: number // One-based visible step number.
  title: string // Displayed section title.
  description?: string // Explanatory text for display.
  done?: boolean // Completed step display state.

  // Removes the connecting line after the final step while retaining the step contents.
  last?: boolean
}>()
</script>
