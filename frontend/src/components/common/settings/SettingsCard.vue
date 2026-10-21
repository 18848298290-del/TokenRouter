<template>
  <section class="card">
    <header class="card-header flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <h2 class="text-lg font-semibold text-primary-900 dark:text-dark-50">
          {{ title }}
        </h2>
        <p v-if="description" class="mt-1 text-sm text-primary-900/80 dark:text-dark-300">
          {{ description }}
        </p>
        <slot name="description" />
      </div>
      <slot name="actions" />
    </header>
    <div class="space-y-6 p-6">
      <slot />
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * SettingsCard.vue groups full page settings beneath a title, description, and
 * optional header actions. The content area supplies shared section spacing; pages own
 * saving and can place refresh or test controls in the actions slot.
 */

/**
 * The card props supply its heading and optional introductory text; description
 * and actions slots let the containing settings page extend the header.
 */
defineProps<{
  title: string /* displayed heading text */
  description?: string /* optional explanatory text */
}>()
</script>
