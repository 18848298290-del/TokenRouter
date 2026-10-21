<template>
  <SettingsSection>
    <!-- The connection layout includes its title, scope, full-width selector, and current option description, matching the image policy control. -->
    <div class="space-y-2">
      <label :for="`${uid}-select`" class="input-label">{{
        t('admin.providers.openai.wsConnectionTitle')
      }}</label>
      <p :id="`${uid}-scope`" class="input-hint">
        {{ t('admin.providers.openai.wsConnectionScope') }}
      </p>
      <Select
        :id="`${uid}-select`"
        :aria-label="t('admin.providers.openai.wsConnectionTitle')"
        :aria-describedby="`${uid}-scope ${uid}-description`"
        :data-testid="`${testIdPrefix}-select`"
        :model-value="modelValue"
        :options="options"
        @update:model-value="emit('update:modelValue', String($event) as ResponsesWSConnectionMode)"
      />
      <p :id="`${uid}-description`" class="input-hint">
        {{ t(responsesWSConnectionHint(modelValue)) }}
      </p>
    </div>
  </SettingsSection>
</template>

<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'

import Select from '@/components/common/Select.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import { useResponsesWSConnectionModeOptions } from '@/components/provider/form/providerFormOptions'
import {
  responsesWSConnectionHint,
  type ResponsesWSConnectionMode,
} from '@/utils/responsesWsConnection'

// ResponsesWSConnectionModeSelector.vue presents provider Responses WebSocket connection
// modes with their current description. Shared option values keep create, edit, and bulk
// forms consistent, and selection events return the chosen mode to the owning form for
// persistence.
withDefaults(
  defineProps<{
    modelValue: ResponsesWSConnectionMode // Parent-owned selected value.
    testIdPrefix?: string // Test selector ID prefix.
  }>(),
  {
    testIdPrefix: 'responses-ws-connection',
  },
)

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  // update:modelValue supplies the selected connection-mode value to the parent provider
  // form. Persistence and connection lifecycle behavior remain with the parent
  // submission and backend handling of that stored setting.
  (event: 'update:modelValue', value: ResponsesWSConnectionMode): void
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
// options supplies selectable values and their display labels to the shared selector
// rendered in the template.
const options = useResponsesWSConnectionModeOptions()
</script>
