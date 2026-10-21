<template>
  <button
    type="button"
    role="radio"
    :aria-checked="selected"
    :disabled="disabled"
    class="flex w-full items-start gap-3 rounded-control border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60"
    :class="
      selected
        ? platformChoiceSelectedClass(accent ?? '')
        : 'border-gray-200 bg-white hover:border-gray-300 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-900 dark:hover:border-dark-500 dark:hover:bg-dark-800'
    "
  >
    <span
      class="flex h-8 w-8 shrink-0 items-center justify-center rounded-control"
      :class="
        selected
          ? platformSolidClass(accent ?? '')
          : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
      "
    >
      <PlatformIcon v-if="platform" :platform="platform" size="sm" />
      <Icon v-else-if="icon" :name="icon" size="sm" :animate-on-hover="false" />
    </span>
    <span class="min-w-0 flex-1">
      <span class="block text-sm font-medium text-primary-900 dark:text-dark-50">{{ title }}</span>
      <span v-if="description" class="block text-xs text-gray-500 dark:text-gray-400">{{
        description
      }}</span>
      <slot />
    </span>
    <slot name="aside" />
  </button>
</template>

<script setup lang="ts">
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import type { IconName } from '@/components/icons/registry'
import type { ProviderPlatform } from '@/api/admin/providers.types'
import { platformChoiceSelectedClass, platformSolidClass } from '@/utils/platformColors'

// ProviderChoiceCard.vue renders a selectable provider option with a title, description,
// and optional icon. A supplied platform selects its platform mark, while the independent
// accent value controls selection colors and disabled state prevents the card button from
// being activated.
defineProps<{
  selected: boolean // Current card selection state.
  title: string // Displayed section title.
  description?: string // Explanatory text for display.
  icon?: IconName // Optional general-purpose icon name.

  // When supplied, renders the platform mark before considering the general-purpose icon
  // prop for this card.
  platform?: ProviderPlatform
  disabled?: boolean // Disables the rendered control.

  // Platform used for selected colors; an omitted value uses the shared theme selection
  // styling.
  accent?: string
}>()
</script>
