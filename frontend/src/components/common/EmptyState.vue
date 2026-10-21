<template>
  <div class="empty-state">
    <div
      class="mb-5 flex h-20 w-20 items-center justify-center rounded-surface bg-gray-100 dark:bg-dark-800"
    >
      <slot name="icon">
        <component v-if="icon" :is="icon" class="empty-state-icon h-10 w-10" aria-hidden="true" />
        <Icon name="inbox" size="md" v-else class="empty-state-icon h-10 w-10" />
      </slot>
    </div>

    <h3 class="empty-state-title">
      {{ displayTitle }}
    </h3>

    <p v-if="description" class="empty-state-description">
      {{ description }}
    </p>

    <div v-if="actionText || $slots.action" class="mt-6">
      <slot name="action">
        <component
          :is="actionTo ? 'RouterLink' : 'button'"
          v-if="actionText"
          :to="actionTo"
          @click="!actionTo && $emit('action')"
          class="btn btn-primary"
        >
          <Icon v-if="actionIcon" name="plus" size="sm" />
          {{ actionText }}
        </component>
      </slot>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Component } from 'vue'
import Icon from '@/components/icons/Icon.vue'

/**
 * EmptyState.vue renders an empty result message with an optional action and icon.
 * Named slots replace the default icon or action, the title defaults to the translated
 * empty label, and action links use RouterLink while buttons emit an action event.
 */

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()

/**
 * Props describes the caller supplied values consumed by EmptyState.
 */
interface Props {
  icon?: Component | string /* custom heading icon */
  title?: string /* displayed heading text */
  description?: string /* optional explanatory text */
  actionText?: string /* default action label */
  actionTo?: string | object /* action navigation destination */
  actionIcon?: boolean /* action plus icon visibility */
  message?: string /* unused message prop */
}

/**
 * props supplies empty state content and the default action; named slots can replace
 * the icon and action presentation.
 */
const props = withDefaults(defineProps<Props>(), {
  description: '',
  actionIcon: true,
})

/**
 * displayTitle prefers a supplied title and otherwise uses the translated empty result
 * heading.
 */
const displayTitle = computed(() => props.title || t('common.noData'))

/**
 * The action event asks the parent to handle the default button when no navigation
 * destination is configured.
 */
defineEmits(['action'])
</script>
