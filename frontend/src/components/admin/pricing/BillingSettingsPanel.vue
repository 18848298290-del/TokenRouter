<template>
  <div class="divide-y divide-gray-200 dark:divide-dark-700" data-testid="billing-settings">
    <section
      v-for="section in sections"
      :key="section.id"
      :aria-labelledby="`billing-section-${section.id}`"
      class="space-y-4 py-5 first:pt-0 last:pb-0"
    >
      <h3
        :id="`billing-section-${section.id}`"
        class="text-sm font-semibold text-gray-900 dark:text-white"
      >
        {{ t(`admin.pricing.billingSettings.platforms.${section.id}`) }}
      </h3>
      <div v-for="key in section.toggles" :key="key" class="flex items-start justify-between gap-4">
        <div class="min-w-0">
          <label :for="`pricing-${key}`" class="text-sm text-gray-900 dark:text-gray-100">
            {{ t(`admin.pricing.billingSettings.${key}`) }}
          </label>
          <p
            :id="`pricing-${key}-hint`"
            class="mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400"
          >
            {{ t(`admin.pricing.billingSettings.hints.${key}`) }}
          </p>
        </div>
        <Toggle
          :id="`pricing-${key}`"
          v-model="settings[key]"
          class="shrink-0"
          :aria-label="t(`admin.pricing.billingSettings.${key}`)"
          :aria-describedby="`pricing-${key}-hint`"
        />
      </div>
      <div
        v-if="section.id === 'general' && settings.peak_rate_enabled"
        class="grid grid-cols-1 gap-4 sm:grid-cols-3"
      >
        <div v-for="key in times" :key="key">
          <label :for="`pricing-${key}`" class="input-label">{{
            t(`admin.pricing.billingSettings.${key}`)
          }}</label>
          <input
            :id="`pricing-${key}`"
            v-model="settings[key]"
            type="time"
            class="input"
            required
          />
        </div>
        <div>
          <label for="pricing-peak_rate_multiplier" class="input-label">{{
            t('admin.pricing.billingSettings.peak_rate_multiplier')
          }}</label>
          <input
            id="pricing-peak_rate_multiplier"
            v-model.number="settings.peak_rate_multiplier"
            type="number"
            min="0"
            step="any"
            required
            class="input"
          />
        </div>
      </div>
      <div v-if="section.prices.length" class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div v-for="key in section.prices" :key="key">
          <label :for="`pricing-${key}`" class="input-label">{{
            t(`admin.pricing.billingSettings.${key}`)
          }}</label>
          <input
            :id="`pricing-${key}`"
            v-model.number="settings[key]"
            type="number"
            min="0"
            step="any"
            :required="key.startsWith('batch_')"
            :aria-describedby="`pricing-${key}-hint`"
            class="input"
            :placeholder="
              key.startsWith('batch_') ? undefined : t('admin.pricing.billingSettings.defaultPrice')
            "
          />
          <p
            :id="`pricing-${key}-hint`"
            class="mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400"
          >
            {{ t(`admin.pricing.billingSettings.hints.${key}`) }}
          </p>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { BillingSettings } from '@/api/admin/pricing.types'

// BillingSettingsPanel.vue groups shared billing controls by the platforms that consume them.
// All values remain in the parent model while section visibility organizes editing. Peak
// periods use time inputs, and optional service prices retain their clearable input
// representation until submission.

// settings binds the parent billing draft, and times identifies the peak interval endpoints
// rendered by the time inputs.
const settings = defineModel<BillingSettings>({ required: true })
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
const times = ['peak_start', 'peak_end'] as const

// sections groups billing controls by their consuming platform while every field remains stored
// in the shared settings model.
const sections = [
  {
    id: 'general',
    toggles: ['long_context_pricing_enabled', 'peak_rate_enabled'],
    prices: [],
  },
  {
    id: 'openai',
    toggles: ['free_openai_fast'],
    prices: ['web_search_price_per_call'],
  },
  {
    id: 'gemini',
    toggles: [],
    prices: ['batch_image_discount_multiplier', 'batch_image_hold_multiplier'],
  },
  {
    id: 'grok',
    toggles: [],
    prices: [
      'search_price_per_1k',
      'audio_realtime_price_per_min',
      'audio_tts_price_per_million_chars',
      'audio_stt_price_per_hour',
    ],
  },
] as const
</script>
