<template>
  <span :class="badgeClass">
    <ProviderIcon :brand="platform" size="14px" />

    <span class="self-baseline">{{ label }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ProviderIcon from '@/components/common/ProviderIcon.vue'
import { resolveProviderBrand } from '@/utils/providerBrand'

/**
 * PlatformBadge.vue displays a provider platform name beside its resolved brand icon
 * in a pill. The translation catalog supplies known labels and the raw identifier
 * handles unknown labels, while provider brand resolution supplies matching badge
 * colors.
 */

/**
 * props supplies the platform identifier used by translation lookup, brand resolution,
 * and the rendered provider icon.
 */
const props = defineProps<{ platform: string /* provider platform identifier */ }>()

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()

/**
 * label translates the platform identifier and retains the raw identifier when its
 * translation is missing.
 */
const label = computed(() => t(`admin.groups.platforms.${props.platform}`, props.platform))
/**
 * badgeClass combines the shared pill geometry with the resolved provider brand
 * palette and its inset border.
 */
const badgeClass = computed(
  () =>
    `inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-semibold ring-1 ring-inset ${resolveProviderBrand(props.platform).badgeClass}`,
)
</script>
