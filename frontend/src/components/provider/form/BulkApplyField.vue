<template>
  <div :class="plain ? 'space-y-2' : 'settings-section space-y-3'">
    <div class="flex items-start gap-3">
      <input
        :id="`${id}-enabled`"
        v-model="apply"
        type="checkbox"
        class="mt-0.5 h-4 w-4 shrink-0"
        :aria-controls="bodyId"
        :data-testid="applyTestid"
      />
      <div class="min-w-0 flex-1">
        <label
          :id="`${id}-label`"
          :for="`${id}-enabled`"
          class="cursor-pointer text-sm font-medium text-primary-900 dark:text-dark-50"
          >{{ label }}</label
        >
        <p v-if="hint" class="input-hint">{{ hint }}</p>
      </div>
      <div
        v-if="$slots.control"
        :id="$slots.default ? undefined : bodyId"
        class="shrink-0 transition-opacity"
        :class="!active && 'opacity-50'"
        :inert="!active || undefined"
      >
        <slot name="control" />
      </div>
    </div>
    <div
      v-if="$slots.default"
      :id="bodyId"
      role="group"
      :aria-labelledby="`${id}-label`"
      class="space-y-3 transition-opacity"
      :class="!active && 'opacity-50'"
      :inert="!active || undefined"
    >
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

// BulkApplyField.vue wraps a bulk-edit field with an application checkbox and an editable
// content slot. The parent uses the checked state when building its patch; this wrapper
// also disables interaction while unchecked or while the caller reports an unmet
// dependency.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  // Base identifier used to connect the application checkbox with its content container
  // IDs.
  id: string
  label: string // Displayed input label.
  hint?: string // Supplemental field help text.

  // Uses the compact grid presentation and omits the surrounding section divider for this
  // item.
  plain?: boolean

  // Keeps the content disabled even when apply is selected because an external dependency
  // is unmet.
  locked?: boolean
  applyTestid?: string // Application checkbox test selector.
}>()
// apply binds the parent-owned bulk patch application switch to the shared controls
// through its model update event.
const apply = defineModel<boolean>({ required: true })
// bodyId links the bulk field content container to its application checkbox through the
// shared base ID.
const bodyId = computed(() => `${props.id}-body`)
// active permits interaction only when the application checkbox is selected and the
// caller has not locked the field.
const active = computed(() => apply.value && !props.locked)
</script>
