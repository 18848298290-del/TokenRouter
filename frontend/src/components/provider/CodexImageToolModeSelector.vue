<template>
  <SettingsSection>
    <!-- The policy layout includes its title, scope, full-width selector, and current option description, matching the corresponding group control. -->
    <div class="space-y-2">
      <template v-if="!hideTitle">
        <label :for="`${uid}-select`" class="input-label">{{
          t('admin.protocols.imagePolicy')
        }}</label>
        <p :id="`${uid}-scope`" class="input-hint">
          {{ t('admin.providers.openai.codexImageToolDesc') }}
        </p>
      </template>
      <Select
        :id="`${uid}-select`"
        :aria-label="t('admin.protocols.imagePolicy')"
        :aria-describedby="hideTitle ? `${uid}-description` : `${uid}-scope ${uid}-description`"
        :data-testid="`${testIdPrefix}-select`"
        :model-value="modelValue"
        :options="options"
        @update:model-value="emit('update:modelValue', String($event) as CodexImageToolMode)"
      />
      <p :id="`${uid}-description`" class="input-hint">{{ descriptions[modelValue] }}</p>
    </div>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'

import Select from '@/components/common/Select.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import type { CodexImageToolMode } from '@/utils/codexImageToolMode'

// CodexImageToolModeSelector.vue presents the provider policy for the Responses image
// tool. It shares labels for enabled, disabled, and blocked policies with group controls,
// while the inherited option explains the provider-specific source of its effective
// setting.
withDefaults(
  defineProps<{
    modelValue: CodexImageToolMode // Parent-owned selected value.
    testIdPrefix?: string // Test selector ID prefix.

    // Hides the section title when an enclosing bulk application row already provides it.
    hideTitle?: boolean
  }>(),
  {
    testIdPrefix: 'codex-image-tool',
  },
)

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  // update:modelValue supplies the selected image-tool policy to the parent provider
  // form. The owner combines this draft with other provider fields when persisting the
  // administrator configuration.
  (event: 'update:modelValue', value: CodexImageToolMode): void
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()

// sharedModes reuses group labels for the three explicit image policies while the
// inherited choice uses provider-specific wording.
const sharedModes = ['enabled', 'disabled', 'block'] as const
// options supplies selectable values and their display labels to the shared selector
// rendered in the template.
const options = computed(() => [
  { value: 'inherit', label: t('admin.providers.openai.codexImageToolInherit') },
  ...sharedModes.map((mode) => ({
    value: mode,
    label: t(`admin.protocols.imagePolicyOptions.${mode}.label`),
  })),
])
// descriptions explains the selected policy beneath the control using the same wire keys
// as the model.
const descriptions = computed<Record<CodexImageToolMode, string>>(() => ({
  inherit: t('admin.providers.openai.codexImageToolInheritDesc'),
  enabled: t('admin.protocols.imagePolicyOptions.enabled.description'),
  disabled: t('admin.protocols.imagePolicyOptions.disabled.description'),
  block: t('admin.protocols.imagePolicyOptions.block.description'),
}))
</script>
