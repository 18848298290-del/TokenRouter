<template>
  <SettingsSubpanel>
    <div>
      <label :for="`${uid}-base`" class="input-label">{{
        t('admin.providers.quotaControl.rpmLimit.baseRpm')
      }}</label>
      <input
        :id="`${uid}-base`"
        v-model.number="baseRpm"
        type="number"
        min="1"
        max="1000"
        step="1"
        class="input"
        :placeholder="t('admin.providers.quotaControl.rpmLimit.baseRpmPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.providers.quotaControl.rpmLimit.baseRpmHint') }}</p>
    </div>

    <div>
      <span class="input-label">{{ t('admin.providers.quotaControl.rpmLimit.strategy') }}</span>
      <SettingsSegmented
        v-model="strategy"
        block
        :ariaLabel="t('admin.providers.quotaControl.rpmLimit.strategy')"
        :options="strategyOptions"
      />
      <p class="input-hint">{{ strategyHint }}</p>
    </div>

    <div v-if="strategy === 'tiered'" v-content-reveal>
      <label :for="`${uid}-buffer`" class="input-label">{{
        t('admin.providers.quotaControl.rpmLimit.stickyBuffer')
      }}</label>
      <input
        :id="`${uid}-buffer`"
        v-model.number="stickyBuffer"
        type="number"
        min="1"
        step="1"
        class="input"
        :placeholder="t('admin.providers.quotaControl.rpmLimit.stickyBufferPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.providers.quotaControl.rpmLimit.stickyBufferHint') }}</p>
    </div>
  </SettingsSubpanel>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import SettingsSegmented from '@/components/common/settings/SettingsSegmented.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import { vContentReveal } from '@/directives/contentReveal'
import type { RpmStrategy } from './providerFormOptions'

// RpmLimitPanel.vue edits the base request rate, enforcement strategy, and sticky-session
// buffer for provider forms. Strategy descriptions follow the selected option, while the
// owner retains the nullable numeric drafts and decides how enabled limits are persisted.

// baseRpm binds the parent-owned base requests-per-minute limit to the shared controls
// through its model update event.
const baseRpm = defineModel<number | null>('baseRpm', { required: true })
// strategy binds the parent-owned request-rate enforcement strategy to the shared
// controls through its model update event.
const strategy = defineModel<RpmStrategy>('strategy', { required: true })
// stickyBuffer binds the parent-owned sticky-session request-rate buffer to the shared
// controls through its model update event.
const stickyBuffer = defineModel<number | null>('stickyBuffer', { required: true })

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()
// strategyOptions presents tiered and sticky-exempt rate enforcement choices with
// localized labels for the segmented selector.
const strategyOptions = computed(() => [
  { value: 'tiered' as const, label: t('admin.providers.quotaControl.rpmLimit.strategyTiered') },
  {
    value: 'sticky_exempt' as const,
    label: t('admin.providers.quotaControl.rpmLimit.strategyStickyExempt'),
  },
])

// strategyHint explains the selected enforcement mode beneath the selector while the
// button labels remain short and comparable.
const strategyHint = computed(() =>
  strategy.value === 'tiered'
    ? t('admin.providers.quotaControl.rpmLimit.strategyTieredHint')
    : t('admin.providers.quotaControl.rpmLimit.strategyStickyExemptHint'),
)
</script>
