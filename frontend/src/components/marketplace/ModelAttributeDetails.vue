<template>
  <HelpTooltip
    trigger="both"
    width-class="w-72"
    :closable="false"
    :interactive="selectable"
    :accessible-label="t('admin.modelAttributes.details')"
    @update:open="onOpenChange"
  >
    <template #trigger="{ open, panelId }">
      <button
        type="button"
        data-testid="model-attributes-trigger"
        class="inline-flex h-5 w-5 items-center justify-center rounded-full text-gray-400 transition-colors hover:text-gray-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-dark-500 dark:hover:text-dark-200"
        :aria-label="t('admin.modelAttributes.details')"
        :aria-expanded="selectable ? open : undefined"
        :aria-controls="panelId"
        :aria-haspopup="selectable ? 'dialog' : undefined"
      >
        <Icon name="infoCircle" size="sm" class="h-4 w-4" />
      </button>
    </template>
    <div class="space-y-3">
      <ModelProtocolChips
        :protocols="model.protocols"
        :native-protocols="model.native_protocols"
        :selectable="selectable"
        :selected="selected"
        @update:selected="selected = $event"
      />
      <ModelAttributesSummary
        v-if="activeAttributes"
        :attributes="activeAttributes"
        :modality-baseline="model.attributes"
        route-differences-key="marketplace.attributeRouteDifferences"
        variant="tooltip"
        class="border-t border-white/10 pt-3 dark:border-dark-600"
      />
    </div>
  </HelpTooltip>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import ModelAttributesSummary from '@/components/common/ModelAttributesSummary.vue'
import Icon from '@/components/icons/Icon.vue'
import ModelProtocolChips from './ModelProtocolChips.vue'
import type { MarketplaceModel } from '@/api/marketplace.types'
import type { ProtocolID } from '@/api/protocols.types'

// ModelAttributeDetails.vue owns one marketplace model's protocol selection and
// attribute panel. Cards retain their overall modality summary while this panel
// switches every attribute to the selected client protocol. Closing the panel
// resets selection so another inspection starts with the full available range.

// The supplied model and localization service provide content for this panel;
// selection stays local and does not alter the parent's marketplace record.
const props = defineProps<{
  model: MarketplaceModel // Public model and protocol display attributes.
}>()
const { t } = useI18n()
// Panel selection is local to this model. Older responses remain static until
// every advertised protocol has an attribute summary, preventing partial selectors.
const selected = ref<ProtocolID | null>(null)
const selectable = computed(
  () =>
    !!props.model.attributes &&
    !!props.model.protocols?.length &&
    !!props.model.protocol_attributes &&
    (props.model.protocols ?? []).every(
      (protocol) => !!props.model.protocol_attributes?.[protocol],
    ),
)
const activeAttributes = computed(() =>
  selected.value && selectable.value
    ? props.model.protocol_attributes?.[selected.value]
    : props.model.attributes,
)

// onOpenChange resets the local protocol when the attribute panel is dismissed.
function onOpenChange(open: boolean) {
  if (!open) selected.value = null
}

// A replacement model reference starts a fresh inspection; nested mutations of
// the same record do not trigger this shallow selection-reset watcher.
watch(
  () => props.model,
  () => {
    selected.value = null
  },
)
</script>
