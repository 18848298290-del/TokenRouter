<template>
  <section class="settings-section space-y-4">
    <div v-if="title" class="flex flex-wrap items-end justify-between gap-3">
      <div class="min-w-0 flex-1">
        <h4 class="text-sm font-semibold text-primary-900 dark:text-dark-50">
          {{ title }}
        </h4>
        <p v-if="hint" class="input-hint">{{ hint }}</p>
      </div>
      <slot name="actions" />
    </div>
    <slot />
  </section>
</template>

<script setup lang="ts">
/**
 * SettingsSection.vue groups settings under an optional title and hint with an actions
 * slot. Adjacent sections receive a shared top divider through an unscoped class rule,
 * allowing custom caller sections to participate in the same spacing scheme.
 */

/**
 * The section props supply an optional heading and hint; a missing heading also
 * leaves the actions header unrendered in this section.
 */
defineProps<{
  title?: string // displayed heading text
  hint?: string /* inline explanation text */
}>()
</script>

<style>
/*
 * This rule is unscoped so caller supplied sections using the same class
 * participate in the adjacent section spacing and divider pattern.
 */
.settings-section + .settings-section {
  @apply border-t border-gray-200 pt-6 dark:border-dark-600;
}
</style>
