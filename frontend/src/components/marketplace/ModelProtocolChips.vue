<template>
  <!-- Protocol chips share the attribute panel palette; native status and full endpoints remain available in hover text. -->
  <div v-if="items.length" data-testid="model-protocols">
    <div class="flex items-center justify-between gap-4">
      <p class="text-gray-400 dark:text-dark-400">{{ t('marketplace.protocols') }}</p>
      <!-- Show the legend only when a displayed protocol uses the corresponding native-protocol color. -->
      <span
        v-if="hasNative"
        class="inline-flex items-center gap-1.5 text-gray-400 dark:text-dark-400"
        data-testid="model-protocols-native-legend"
      >
        <span
          class="h-2.5 w-2.5 rounded-full bg-emerald-500/20 ring-1 ring-inset ring-emerald-400/70"
        ></span>
        {{ t('marketplace.protocolNative') }}
      </span>
    </div>
    <ul class="mt-2 flex flex-wrap gap-1.5">
      <li v-if="selectable">
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded-compact bg-white/5 px-1.5 py-0.5 text-white transition-colors duration-fast hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-400 dark:bg-dark-800"
          :aria-pressed="selected === null"
          data-protocol="all"
          @click="emit('update:selected', null)"
        >
          <Icon v-if="selected === null" name="check" size="xs" />
          {{ t('marketplace.allProtocols') }}
        </button>
      </li>
      <!-- Native protocols use a green outline and fill; converted protocols retain the neutral panel background. -->
      <li
        v-for="protocol in items"
        :key="protocol.id"
        class="inline-flex items-center gap-1 rounded-compact px-1.5 py-0.5 text-white dark:text-dark-100"
        :class="
          protocol.native
            ? 'bg-emerald-500/15 ring-1 ring-inset ring-emerald-400/60'
            : 'bg-white/5 dark:bg-dark-800'
        "
        :data-protocol="protocol.id"
        :data-native="protocol.native"
        :title="`${protocol.endpoint} · ${t(protocol.native ? 'marketplace.protocolNativeHint' : 'marketplace.protocolConvertedHint')}`"
      >
        <component
          :is="selectable ? 'button' : 'span'"
          :type="selectable ? 'button' : undefined"
          class="inline-flex items-center gap-1 rounded-compact transition-colors duration-fast focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-400"
          :aria-pressed="selectable ? selected === protocol.id : undefined"
          @click="selectable && emit('update:selected', protocol.id)"
        >
          <Icon v-if="selectable && selected === protocol.id" name="check" size="xs" />
          <!-- OpenAI uses the panel text color so its monochrome mark stays visible on both themes. -->
          <ProviderIcon
            :brand="protocol.brand"
            size="14px"
            :color="protocol.brand === 'openai' ? 'currentColor' : ''"
            class="select-none"
          />
          <span class="whitespace-nowrap">{{ t(`marketplace.protocolNames.${protocol.id}`) }}</span>
        </component>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ProviderIcon from '@/components/common/ProviderIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { marketplaceProtocols } from '@/utils/marketplaceProtocols'
import type { ProtocolID } from '@/api/protocols.types'

// ModelProtocolChips.vue lists public client protocols and their native status.
// Selectable chips expose a local single-protocol view with an all-protocol reset;
// static labels remain available when an older API provides no protocol attributes.

// Display inputs let the owning panel opt into controlled selection while
// protocol order and endpoint metadata continue to come from the shared catalog.
const props = defineProps<{
  // Advertised client protocol identifiers; omission produces no chips, and unknown identifiers are omitted.
  protocols?: ProtocolID[]
  // Enables selection when the owning panel has complete per-protocol attributes for this model.
  selectable?: boolean
  // Null selects the overall union; an identifier marks the currently selected client protocol.
  selected?: ProtocolID | null
  // Subset of advertised protocols that reach the upstream without an intervening protocol conversion.
  nativeProtocols?: ProtocolID[]
}>()
// Protocol controls emit a local selection; catalogue order and native styling
// remain shared between interactive summaries and legacy static protocol labels.
const emit = defineEmits<{
  // Requests a protocol selection; null restores the all-protocol summary in the owning panel.
  'update:selected': [ProtocolID | null]
}>()
const { t } = useI18n()

// Catalog-resolved items carry native flags only for identifiers in the supplied
// subset; the legend reflects the resulting visible list rather than raw input.
const items = computed(() => {
  const native = new Set(props.nativeProtocols ?? [])
  return marketplaceProtocols(props.protocols).map((protocol) => ({
    ...protocol,
    native: native.has(protocol.id),
  }))
})
const hasNative = computed(() => items.value.some((protocol) => protocol.native))
</script>
